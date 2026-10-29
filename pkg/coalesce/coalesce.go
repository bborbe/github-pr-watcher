// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package coalesce bounds how many review records a burst of pushes to one
// pull request can create.
//
// The mechanism is a trailing coalesce: the first push of a burst is dispatched
// immediately, so a lone push pays no added latency at all; every push behind
// it collapses into a single trailing dispatch that fires once the PR has been
// quiet for the hold window. A six-push burst therefore yields at most two
// records — the leading one and the trailing one — against the five the
// pipeline produced before this package existed (bborbe-nuke#815, 2026-10-27).
//
// A full debounce would yield exactly one record, but it must hold the first
// push too, which adds the hold window to every PR including a lone one; a
// 60 s hold roughly doubles the measured p50 of 36 s and pushes the goal's p90
// past its 10-minute bar. That trade is why the leading edge is unconditional.
//
// Nothing is recorded as emitted until the caller confirms the publish with
// MarkEmitted. A decision is therefore a decision, not a commitment: a failed
// send leaves the head unclaimed so the next webhook retry or the next poll
// re-admits it, instead of a lost record being silently recorded as done.
package coalesce

import (
	"context"
	"strconv"
	"sync"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// Key identifies one pull request across every emit path. The poll loop and the
// webhook receiver build it independently, so both must agree on the fields.
type Key struct {
	Owner  string
	Repo   string
	Number int
}

// String renders the key the way the pipeline logs a PR elsewhere.
func (k Key) String() string {
	return k.Owner + "/" + k.Repo + "#" + strconv.Itoa(k.Number)
}

// DispatchFunc publishes a review request for one PR URL. It is the webhook
// receiver's own dispatch path, injected so the trailing edge reuses it rather
// than duplicating the command construction.
type DispatchFunc func(ctx context.Context, url string) error

//counterfeiter:generate -o ../../mocks/timer.go --fake-name Timer . Timer

// Timer is the handle a scheduled callback can be cancelled through.
type Timer interface {
	// Stop prevents the callback from running if it has not run yet.
	Stop()
}

//counterfeiter:generate -o ../../mocks/scheduler.go --fake-name Scheduler . Scheduler

// Scheduler schedules a one-shot callback. The production implementation is
// stdtime.AfterFunc; tests inject a manual scheduler so no test sleeps.
type Scheduler interface {
	AfterFunc(d stdtime.Duration, f func()) Timer
}

// Decision is what a Coalescer tells a caller to do with one observation.
//
// It is deliberately three-valued. A boolean conflates "hold this head for the
// trailing dispatch" with "this head was already published", and the two demand
// opposite cursor handling: a held head must not advance the poll cursor (it is
// still owed a record), while an already-published head must, or the search
// window pins to that PR for the process lifetime.
type Decision int

const (
	// Emit means publish a record for this head now, then call MarkEmitted.
	Emit Decision = iota
	// Hold means the head is being coalesced into the trailing dispatch:
	// publish nothing, retry on the next cycle, and do not advance the cursor
	// past this PR.
	Hold
	// Settled means a record for this head was already published: publish
	// nothing, and treat the PR as handled so the cursor advances.
	Settled
)

// String renders the decision for logs.
func (d Decision) String() string {
	switch d {
	case Emit:
		return "emit"
	case Hold:
		return "hold"
	case Settled:
		return "settled"
	default:
		return "unknown"
	}
}

//counterfeiter:generate -o ../../mocks/coalescer.go --fake-name Coalescer . Coalescer

// Coalescer decides whether a review record may be emitted for a PR's head.
//
// Both emit paths consult the same instance, because a gate on either alone
// still lets a burst produce a third record through the other: the webhook
// receiver emits per push, and the poll loop emits per (PR, SHA) it has not
// seen before.
type Coalescer interface {
	// Push is the webhook-side entry point for a push delivery. It returns true
	// when the caller must publish now (the leading edge) and false when the
	// push was coalesced and a single trailing dispatch is scheduled for hold
	// after the last push seen for this PR.
	Push(key Key, headSHA string, url string, now libtime.DateTime) bool

	// Admit is the poll-side entry point. pushAt is the time of the push that
	// produced headSHA — GitHub's updated_at — so the poll can tell a quiet head
	// from one still inside a burst.
	Admit(key Key, headSHA string, pushAt libtime.DateTime, now libtime.DateTime) Decision

	// MarkEmitted records that a record for headSHA was actually published.
	// Callers invoke it only after a successful publish, so a failed send
	// leaves the head claimable.
	MarkEmitted(key Key, headSHA string, now libtime.DateTime)

	// Close stops every scheduled trailing dispatch. Call it on shutdown.
	Close()
}

// maxEntries bounds the per-PR state map. One entry accumulates per PR ever
// seen, so without a bound a long-running watcher grows without limit.
const maxEntries = 4096

// MaxHold is the largest accepted hold window. Beyond it a misconfiguration
// silently starves every burst of its trailing record rather than failing.
const MaxHold = stdtime.Hour

// NewCoalescer returns a Coalescer with the given hold window.
//
// ctx is the process lifetime the trailing dispatch runs under; the dispatch
// never runs on context.Background(), so shutting the watcher down cancels a
// pending trailing publish rather than leaving it in flight. hold is how long a
// PR must stay quiet before its trailing dispatch fires; it should exceed the
// gap between pushes inside a burst and no more. dispatch is invoked from a
// timer goroutine, never from the caller's goroutine.
func NewCoalescer(
	ctx context.Context,
	hold stdtime.Duration,
	scheduler Scheduler,
	dispatch DispatchFunc,
	clock libtime.CurrentDateTimeGetter,
) Coalescer {
	return &coalescer{
		ctx:       ctx,
		hold:      hold,
		scheduler: scheduler,
		dispatch:  dispatch,
		clock:     clock,
		entries:   make(map[Key]*entry),
	}
}

type coalescer struct {
	ctx       context.Context
	hold      stdtime.Duration
	scheduler Scheduler
	dispatch  DispatchFunc
	clock     libtime.CurrentDateTimeGetter

	mu      sync.Mutex
	entries map[Key]*entry
}

// entry is the per-PR coalescing state.
type entry struct {
	// lastEmitAt is when a record was last published for this PR, by either
	// path. A zero value means nothing has been published in this process,
	// which is what makes the first push a leading one.
	lastEmitAt libtime.DateTime
	// lastHead is the head last published for this PR, used to settle a head
	// that is already done rather than emit it twice.
	lastHead string
	// lastPushAt is the most recent push observed for this PR, by either path.
	// A leading dispatch requires the PR to have been quiet since then —
	// without it, a burst longer than the hold window would admit a fresh
	// leading record every hold and defeat the coalesce.
	lastPushAt libtime.DateTime
	// pendingHead and pendingURL describe the trailing dispatch, if one is
	// scheduled. pendingURL being empty means no dispatch is outstanding.
	pendingHead string
	pendingURL  string
	// generation identifies the scheduled timer. A firing timer whose
	// generation no longer matches was superseded by a later push and must not
	// publish — URL equality cannot tell two timers for the same PR apart,
	// because every push to a PR carries the same html_url.
	generation uint64
	timer      Timer
}

// Push implements Coalescer.
func (c *coalescer) Push(
	key Key,
	headSHA string,
	url string,
	now libtime.DateTime,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.prune(now)
	e := c.entry(key)
	// A repeated delivery of a head already published (GitHub retries webhooks)
	// must not dispatch twice.
	if headSHA != "" && headSHA == e.lastHead {
		return false
	}

	leading := e.lastEmitAt.IsZero() ||
		(now.Sub(e.lastPushAt).Duration() >= c.hold &&
			now.Sub(e.lastEmitAt).Duration() >= c.hold)
	if e.lastPushAt.IsZero() || now.After(e.lastPushAt) {
		e.lastPushAt = now
	}
	if leading {
		c.clearPending(e)
		return true
	}

	c.schedule(e, key, headSHA, url)
	glog.V(2).Infof("coalesced pr=%s sha=%s hold=%s", key, headSHA, c.hold)
	return false
}

// Admit implements Coalescer.
func (c *coalescer) Admit(
	key Key,
	headSHA string,
	pushAt libtime.DateTime,
	now libtime.DateTime,
) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.prune(now)
	e := c.entry(key)
	// Only ever advance lastPushAt. A poll response can predate a push the
	// webhook already saw, and moving the clock backwards would make the PR
	// look quiet and hand the next push a fresh leading edge.
	if !pushAt.IsZero() && (e.lastPushAt.IsZero() || pushAt.After(e.lastPushAt)) {
		e.lastPushAt = pushAt
	}
	if headSHA == "" {
		return Hold
	}
	if headSHA == e.lastHead {
		return Settled
	}
	// A trailing dispatch is already scheduled for this PR: it will publish the
	// head that finally goes quiet, and a poll emission now would be the third
	// record of the burst.
	if e.pendingURL != "" {
		return Hold
	}
	// Nothing has been published for this PR in this process, so this head is
	// the leading record. That is what keeps a poll-only deployment — and the
	// first PR seen after a restart — free of the hold window; applying the
	// quiet rule here would add it to every PR.
	if e.lastEmitAt.IsZero() {
		return Emit
	}
	// The head is still moving — the PR was pushed less than hold ago. The test
	// uses lastPushAt, the newest push either path has seen, not this
	// observation's pushAt: a poll response can carry an updated_at older than a
	// push the webhook already saw, and trusting it would make an actively
	// pushed PR look quiet.
	if !e.lastPushAt.IsZero() && now.Sub(e.lastPushAt).Duration() < c.hold {
		return Hold
	}
	// A record was published inside the hold window; a second one now would
	// defeat the coalesce.
	if now.Sub(e.lastEmitAt).Duration() < c.hold {
		return Hold
	}
	return Emit
}

// MarkEmitted implements Coalescer.
func (c *coalescer) MarkEmitted(key Key, headSHA string, now libtime.DateTime) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.entry(key)
	e.lastEmitAt = now
	e.lastHead = headSHA
	c.clearPending(e)
}

// Close implements Coalescer.
//
// It stops every scheduled timer unconditionally. It deliberately does NOT
// consult c.ctx: Close runs on the shutdown path, where the process context is
// already cancelled, and stopping timers is exactly what shutdown needs.
func (c *coalescer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range c.entries {
		c.stopTimer(e)
	}
}

// fire runs the trailing dispatch for one scheduled timer.
func (c *coalescer) fire(key Key, url string, generation uint64) {
	c.mu.Lock()
	e, found := c.entries[key]
	if !found || e.generation != generation {
		// Superseded by a later push; that push owns the pending state.
		c.mu.Unlock()
		return
	}
	headSHA := e.pendingHead
	e.pendingURL = ""
	e.pendingHead = ""
	e.timer = nil
	c.mu.Unlock()

	if err := c.dispatch(c.ctx, url); err != nil {
		glog.Errorf("trailing dispatch failed pr=%s err=%v", key, err)
		// Re-arm rather than mark the head emitted: a record that was never
		// published must stay claimable, or a webhook retry and the poll both
		// suppress it and the burst's review is lost.
		c.mu.Lock()
		if e, found := c.entries[key]; found && e.pendingURL == "" {
			c.schedule(e, key, headSHA, url)
		}
		c.mu.Unlock()
		return
	}

	c.MarkEmitted(key, headSHA, c.clock.Now())
	glog.V(2).Infof("trailing dispatch pr=%s sha=%s", key, headSHA)
}

// schedule arms the trailing dispatch for key, replacing any pending one.
// Callers hold mu.
func (c *coalescer) schedule(e *entry, key Key, headSHA string, url string) {
	e.pendingHead = headSHA
	e.pendingURL = url
	c.stopTimer(e)
	e.generation++
	generation := e.generation
	e.timer = c.scheduler.AfterFunc(c.hold, func() {
		c.fire(key, url, generation)
	})
}

// prune bounds the state map. It runs only once the map is over maxEntries, and
// drops entries with no outstanding dispatch whose last publish is older than
// the hold window. Callers hold mu.
func (c *coalescer) prune(now libtime.DateTime) {
	if len(c.entries) <= maxEntries {
		return
	}
	for key, e := range c.entries {
		if e.pendingURL != "" {
			continue
		}
		if now.Sub(e.lastEmitAt).Duration() >= c.hold {
			delete(c.entries, key)
		}
	}
}

// entry returns the state for key, creating it on first use. Callers hold mu.
func (c *coalescer) entry(key Key) *entry {
	e, found := c.entries[key]
	if !found {
		e = &entry{}
		c.entries[key] = e
	}
	return e
}

// stopTimer cancels a scheduled trailing dispatch. Callers hold mu.
func (c *coalescer) stopTimer(e *entry) {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// clearPending drops an outstanding trailing dispatch without running it.
// Callers hold mu.
func (c *coalescer) clearPending(e *entry) {
	c.stopTimer(e)
	e.pendingURL = ""
	e.pendingHead = ""
	e.generation++
}

// NewTimerScheduler returns the production Scheduler, backed by time.AfterFunc.
func NewTimerScheduler() Scheduler {
	return &timerScheduler{}
}

type timerScheduler struct{}

func (s *timerScheduler) AfterFunc(d stdtime.Duration, f func()) Timer {
	return &stdTimer{timer: stdtime.AfterFunc(d, f)}
}

// stdTimer adapts *stdtime.Timer's Stop() bool to this package's Timer.
type stdTimer struct {
	timer *stdtime.Timer
}

func (t *stdTimer) Stop() {
	t.timer.Stop()
}

// ValidateHold rejects a hold window that cannot bound a burst, or one so large
// that a misconfiguration would silently starve every burst of its trailing
// record: a non-positive hold would admit every push, which is the behaviour
// this package exists to remove.
func ValidateHold(ctx context.Context, hold stdtime.Duration) error {
	if hold <= 0 {
		return errors.Errorf(ctx, "coalesce hold must be positive, got %s", hold)
	}
	if hold > MaxHold {
		return errors.Errorf(ctx, "coalesce hold must not exceed %s, got %s", MaxHold, hold)
	}
	return nil
}
