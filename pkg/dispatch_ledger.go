// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "sync"

// DispatchLedger records which review task ids this process has observed being
// dispatched — that is, a task event carrying job_started_at, which the executor
// writes when it spawns the Job.
//
// It exists to separate two outcomes the scanner could not previously tell apart.
// A publish that the executor never picks up and a publish whose review simply
// has not finished both leave the PR unreviewed, so the retry loop re-published
// both and reported success either way. That is the silent half of the defect
// this instrument exists to close: the scanner's own success signal said
// "published", and nothing anywhere said "and nothing ever ran".
//
// The ledger is deliberately in-process and never persisted. A restarted pod must
// not inherit another process's publishes as undispatched, or the count
// manufactures the same false positive from the other side — reporting a healthy
// pipeline as broken, which is the failure mode the original defect had in
// reverse. The scoping therefore lives in the CALLER: a candidate is only counted
// undispatched when this process also published it (retryState holds an attempt),
// so a cold start reports zero until it publishes something itself.
type DispatchLedger interface {
	// Mark records that taskID was observed dispatched.
	Mark(taskID string)
	// Seen reports whether taskID was observed dispatched by this process.
	Seen(taskID string) bool
}

// NewDispatchLedger returns an empty in-process ledger.
func NewDispatchLedger() DispatchLedger {
	return &dispatchLedger{seen: map[string]struct{}{}}
}

type dispatchLedger struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

func (l *dispatchLedger) Mark(taskID string) {
	if taskID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[taskID] = struct{}{}
}

func (l *dispatchLedger) Seen(taskID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.seen[taskID]
	return ok
}
