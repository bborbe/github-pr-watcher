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

//counterfeiter:generate -o ../../mocks/coalescer.go --fake-name Coalescer . Coalescer

// Coalescer decides whether a review record may be emitted for a PR's head.
//
// Both emit paths consult the same instance, because a gate on either alone
// still lets a burst produce a third record through the other: the webhook
// receiver emits per push, and the poll loop emits per (PR, SHA) it has not
// seen before.
type Coalescer interface {
	// Push is the webhook-side entry point. It returns true when the caller
	// must dispatch now (the leading edge). When it returns false the push was
	// coalesced and a single trailing dispatch is scheduled for hold after the
	// last push seen for this PR.
	Push(key Key, headSHA string, url string, now libtime.DateTime) bool

	// Admit is the poll-side entry point. It returns true when the poll loop
	// may emit a record for headSHA. pushAt is the time of the push that
	// produced that head — GitHub's updated_at — so the poll can tell a quiet
	// head from one still inside a burst.
	Admit(key Key, headSHA string, pushAt libtime.DateTime, now libtime.DateTime) bool

	// Close stops every scheduled trailing dispatch. Call it on shutdown.
	Close()
}

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
	// lastEmitAt is when a record was last admitted for this PR, by either
	// path. A zero value means nothing has been emitted in this process, which
	// is what makes the first push a leading one.
	lastEmitAt libtime.DateTime
	// lastHead is the head last admitted for this PR, used to suppress a
	// second record for a head that has already been emitted.
	lastHead string
	// lastPushAt is when the previous push for this PR was observed, by either
	// path. A leading dispatch requires the PR to have been quiet since then —
	// without it, a burst longer than the hold window would admit a fresh
	// leading record every hold and defeat the coalesce.
	lastPushAt libtime.DateTime
	// pendingHead and pendingURL describe the trailing dispatch, if one is
	// scheduled. pendingURL being empty means no dispatch is outstanding.
	pendingHead string
	pendingURL  string
	timer       Timer
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

	e := c.entry(key)
	// A repeated delivery of the same head (GitHub retries webhooks) must not
	// dispatch twice.
	if headSHA != "" && headSHA == e.lastHead {
		return false
	}

	leading := e.lastEmitAt.IsZero() ||
		(now.Sub(e.lastPushAt).Duration() >= c.hold &&
			now.Sub(e.lastEmitAt).Duration() >= c.hold)
	e.lastPushAt = now
	if leading {
		e.lastEmitAt = now
		e.lastHead = headSHA
		c.clearPending(e)
		return true
	}

	e.pendingHead = headSHA
	e.pendingURL = url
	c.stopTimer(e)
	e.timer = c.scheduler.AfterFunc(c.hold, func() {
		c.fire(key, url)
	})
	glog.V(2).Infof("coalesced pr=%s sha=%s hold=%s", key, headSHA, c.hold)
	return false
}

// Admit implements Coalescer.
func (c *coalescer) Admit(
	key Key,
	headSHA string,
	pushAt libtime.DateTime,
	now libtime.DateTime,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.entry(key)
	if !pushAt.IsZero() {
		e.lastPushAt = pushAt
	}
	if headSHA == "" || headSHA == e.lastHead {
		return false
	}
	// A trailing dispatch is already scheduled for this PR: it will emit the
	// head that finally goes quiet, and a poll emission now would be the third
	// record of the burst.
	if e.pendingURL != "" {
		return false
	}
	// Nothing has been emitted for this PR in this process, so this head is the
	// leading record and is admitted at once. That is what keeps a poll-only
	// deployment — and the first PR seen after a restart — free of the hold
	// window; applying the quiet rule here would add it to every PR.
	if e.lastEmitAt.IsZero() {
		e.lastEmitAt = now
		e.lastHead = headSHA
		return true
	}
	// The head is still moving — the PR was pushed less than hold ago.
	if !pushAt.IsZero() && now.Sub(pushAt).Duration() < c.hold {
		return false
	}
	// A record was emitted inside the hold window; a second one now would
	// defeat the coalesce.
	if now.Sub(e.lastEmitAt).Duration() < c.hold {
		return false
	}

	e.lastEmitAt = now
	e.lastHead = headSHA
	return true
}

// Close implements Coalescer.
func (c *coalescer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, e := range c.entries {
		select {
		case <-c.ctx.Done():
			return
		default:
		}
		c.stopTimer(e)
	}
}

// fire runs the trailing dispatch. It re-checks the entry under the lock so a
// push that arrived while the timer was firing supersedes it.
func (c *coalescer) fire(key Key, url string) {
	c.mu.Lock()
	e, found := c.entries[key]
	if !found || e.pendingURL != url {
		c.mu.Unlock()
		return
	}
	headSHA := e.pendingHead
	e.pendingURL = ""
	e.pendingHead = ""
	e.timer = nil
	e.lastEmitAt = c.clock.Now()
	e.lastHead = headSHA
	c.mu.Unlock()

	if err := c.dispatch(c.ctx, url); err != nil {
		glog.Errorf("trailing dispatch failed pr=%s err=%v", key, err)
	}
	glog.V(2).Infof("trailing dispatch pr=%s sha=%s", key, headSHA)
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

// ValidateHold rejects a hold window that cannot bound a burst: a non-positive
// hold would admit every push, which is the behaviour this package exists to
// remove.
func ValidateHold(ctx context.Context, hold stdtime.Duration) error {
	if hold <= 0 {
		return errors.Errorf(ctx, "coalesce hold must be positive, got %s", hold)
	}
	return nil
}
