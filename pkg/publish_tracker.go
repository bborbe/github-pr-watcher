// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sync"

	libtime "github.com/bborbe/time"
)

// PublishTracker records the last successful review-task publish, so the watcher
// can tell an idle publish path from a stopped one.
//
// A publish path that logs only what it publishes reports nothing when it
// publishes nothing, and that is exactly what a dead path looks like: measured
// 2026-11-07 on bborbe/nuke#1104, the watcher's publish line was silent for a
// 30-minute window while the unreviewed scanner kept listing the PR on every
// cycle. The tracker makes the silence itself observable — the watcher compares
// the count before and after a cycle and logs the zero together with the
// population the cycle saw, so "no work to do" and "nothing happened" read
// differently.
type PublishTracker struct {
	mu              sync.Mutex
	currentDateTime libtime.CurrentDateTimeGetter
	count           int64
	lastPublish     libtime.DateTime
}

// NewPublishTracker returns a PublishTracker reading the injected clock.
func NewPublishTracker(currentDateTime libtime.CurrentDateTimeGetter) *PublishTracker {
	return &PublishTracker{currentDateTime: currentDateTime}
}

// Record notes one successful publish. Nil-tolerant, so a publisher or watcher
// built without a tracker behaves exactly as before.
func (t *PublishTracker) Record() {
	if t == nil || t.currentDateTime == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	t.lastPublish = t.currentDateTime.Now()
}

// Snapshot returns the number of publishes recorded so far and the time of the
// last one. The time is the zero DateTime when nothing has been published yet.
func (t *PublishTracker) Snapshot() (int64, libtime.DateTime) {
	if t == nil {
		return 0, libtime.DateTime{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count, t.lastPublish
}
