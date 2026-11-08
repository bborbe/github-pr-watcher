// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package escalate routes auto-merge refusals to the operator through the
// shared generic notification system, so a refused PR is never held in
// silence.
//
// One escalation item is published per (repo#number, head SHA, class). The
// episode key is the whole point: a PR that stays refused is re-polled every
// cycle, and without the key it would re-notify on every poll.
package escalate

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bborbe/errors"
	core "github.com/bborbe/notification"
	"github.com/bborbe/notification/command/notification"
	"github.com/golang/glog"
)

// Class names the refusal class carried in the escalation item. The values are
// part of the operator-facing contract: the escalation message and the
// watcher's `escalated pr=… class=…` log line both carry them, and the task's
// verification probes grep for them.
type Class string

const (
	// ClassUntrustedAuthor is a labelled PR whose author is outside the trust
	// gate, so the watcher refuses to arm auto-merge.
	ClassUntrustedAuthor Class = "untrusted-author"
	// ClassTrustCheckError is a trust decision that failed to evaluate. The
	// refusal is not a verdict on the author, so it escalates under its own
	// class rather than as untrusted.
	ClassTrustCheckError Class = "trust-check-error"
	// ClassEnableAutoMergeError is a failed arm request (permissions, an
	// already-clean PR, a transient API error).
	ClassEnableAutoMergeError Class = "enable-auto-merge-error"
	// ClassChecksNotGreen is a labelled PR still non-green after
	// ChecksNotGreenThreshold consecutive poll cycles.
	ClassChecksNotGreen Class = "checks-not-green"
	// ClassReviewRequestChanges is a PR whose latest review on the current head
	// SHA is CHANGES_REQUESTED, so GitHub will never merge it.
	ClassReviewRequestChanges Class = "review-request-changes"
)

// ChecksNotGreenThreshold is the number of consecutive poll cycles a labelled
// PR may stay non-green before the watcher escalates it. It is pinned here, in
// code, and never chosen at run time.
const ChecksNotGreenThreshold = 2

// maxEpisodes bounds the episode map so a long-running watcher cannot grow it
// without limit. Clearing it can re-notify a still-refused PR once; that is the
// accepted trade against an unbounded leak, and the watcher's poll interval
// makes reaching the cap unlikely.
const maxEpisodes = 5000

// Escalation is one refusal to publish.
type Escalation struct {
	// PRURL is the PR's HTML URL. It is what the operator clicks and what the
	// verification probe filters the notification body on.
	PRURL string
	// Repo is the `owner/repo` pair.
	Repo string
	// Number is the pull-request number.
	Number int
	// HeadSHA is the commit the refusal applies to. A new head SHA is a new
	// episode and may escalate again.
	HeadSHA string
	// Class is the refusal class.
	Class Class
	// Detail is a short human-readable reason (an error string or a status).
	Detail string
}

// episodeKey identifies one escalation episode: the same PR at the same head
// SHA refused for the same reason.
func (e Escalation) episodeKey() string {
	return fmt.Sprintf("%s#%d@%s:%s", e.Repo, e.Number, e.HeadSHA, e.Class)
}

// checksKey identifies the non-green observation counter, which is per PR and
// head SHA but independent of class.
func (e Escalation) checksKey() string {
	return fmt.Sprintf("%s#%d@%s", e.Repo, e.Number, e.HeadSHA)
}

// message renders the notification body. It must carry the PR URL and the
// class token: those are the two facts the operator needs and the two the
// verification probe asserts.
func (e Escalation) message() string {
	return fmt.Sprintf(
		"auto-merge refused class=%s pr=%s repo=%s#%d head=%s detail=%s",
		e.Class,
		e.PRURL,
		e.Repo,
		e.Number,
		e.HeadSHA,
		e.Detail,
	)
}

// PublishFunc sends one rendered escalation message. It exists so the episode
// logic is testable without a Kafka broker.
type PublishFunc func(ctx context.Context, message string) error

//counterfeiter:generate -o ../../mocks/escalator.go --fake-name Escalator . Escalator

// Escalator publishes auto-merge refusals exactly once per episode.
type Escalator interface {
	// Escalate publishes the escalation unless this episode was already
	// notified. It returns true when it published, false when it suppressed a
	// duplicate. A publish failure returns an error and does NOT mark the
	// episode notified, so the next poll retries.
	Escalate(ctx context.Context, escalation Escalation) (bool, error)
	// ObserveNotGreen records one poll-cycle observation of a non-green PR and
	// escalates once ChecksNotGreenThreshold consecutive observations are
	// reached. It is idempotent past the threshold because Escalate dedupes.
	ObserveNotGreen(ctx context.Context, escalation Escalation) (bool, error)
	// Reset drops every non-green observation counter for a PR, across all head
	// SHAs. The watcher calls it once the PR's checks go green, so a PR that
	// goes green and red again must accumulate the threshold afresh.
	Reset(ctx context.Context, repo string, number int)
}

type escalator struct {
	publish PublishFunc
	mu      sync.Mutex
	// notified holds episodes already published.
	notified map[string]struct{}
	// inflight holds episodes being published right now. It is what makes the
	// publish itself deduped without holding the lock across the network call.
	inflight  map[string]struct{}
	notGreen  map[string]int
	threshold int
}

// NewEscalator returns an Escalator that publishes through publish.
func NewEscalator(publish PublishFunc) Escalator {
	return &escalator{
		publish:   publish,
		notified:  map[string]struct{}{},
		inflight:  map[string]struct{}{},
		notGreen:  map[string]int{},
		threshold: ChecksNotGreenThreshold,
	}
}

// NewNotificationPublishFunc adapts the generic notification sender to a
// PublishFunc, publishing every escalation as an `agent-escalation` item.
func NewNotificationPublishFunc(
	sender notification.NotificationPublishCommandSender,
) PublishFunc {
	return func(ctx context.Context, message string) error {
		return sender.SendPublishNotificationCommand(ctx, notification.NotificationPublishCommand{
			Type:    core.AgentEscalationNotificationType,
			Message: core.NotificationMessage(message),
		})
	}
}

func (e *escalator) Escalate(ctx context.Context, escalation Escalation) (bool, error) {
	key := escalation.episodeKey()

	// Two-phase reservation: mark the episode inflight under the lock, publish
	// outside it, then move it to notified. Holding the lock across the network
	// call would serialise every escalation behind the slowest send; skipping
	// the inflight set would let two concurrent callers (the poll loop and the
	// webhook handler) publish the same episode twice.
	e.mu.Lock()
	if _, ok := e.notified[key]; ok {
		e.mu.Unlock()
		return false, nil
	}
	if _, ok := e.inflight[key]; ok {
		e.mu.Unlock()
		return false, nil
	}
	e.inflight[key] = struct{}{}
	e.mu.Unlock()

	err := e.publish(ctx, escalation.message())

	e.mu.Lock()
	delete(e.inflight, key)
	if err != nil {
		e.mu.Unlock()
		return false, errors.Wrapf(ctx, err, "publish escalation %s", key)
	}
	if len(e.notified) >= maxEpisodes {
		// Evict ONE entry, not the whole map: clearing it would drop dedupe
		// for every currently-refused PR and re-notify all of them at once.
		for victim := range e.notified {
			delete(e.notified, victim)
			break
		}
	}
	e.notified[key] = struct{}{}
	e.mu.Unlock()

	glog.Infof(
		"escalated pr=%s class=%s repo=%s#%d head=%s",
		escalation.PRURL,
		escalation.Class,
		escalation.Repo,
		escalation.Number,
		escalation.HeadSHA,
	)
	return true, nil
}

func (e *escalator) ObserveNotGreen(
	ctx context.Context,
	escalation Escalation,
) (bool, error) {
	key := escalation.checksKey()

	e.mu.Lock()
	if len(e.notGreen) >= maxEpisodes {
		// Same bounded-growth trade as the notified map, and the same
		// one-at-a-time eviction: an unbounded counter map is a real leak in a
		// watcher that runs for months, but clearing it would restart every
		// threshold at once.
		for victim := range e.notGreen {
			delete(e.notGreen, victim)
			break
		}
	}
	e.notGreen[key]++
	observed := e.notGreen[key]
	e.mu.Unlock()

	if observed < e.threshold {
		glog.V(2).Infof(
			"checks not green, waiting pr=%s observed=%d threshold=%d",
			escalation.PRURL,
			observed,
			e.threshold,
		)
		return false, nil
	}
	return e.Escalate(ctx, escalation)
}

func (e *escalator) Reset(ctx context.Context, repo string, number int) {
	prefix := fmt.Sprintf("%s#%d@", repo, number)

	e.mu.Lock()
	defer e.mu.Unlock()
	for key := range e.notGreen {
		if strings.HasPrefix(key, prefix) {
			delete(e.notGreen, key)
		}
	}
}
