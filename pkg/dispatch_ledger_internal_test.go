// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "testing"

// TestCountUndispatchedScopesToPublishesThisProcessMade pins the scoping that
// keeps the new counter honest.
//
// The counter exists to separate "published and never dispatched" from
// "published and still running". It cannot do that from the scanner's own state
// alone: retryState holds only publishes THIS process made, so a pod that just
// started knows nothing about the tasks already in flight. Without the guard this
// test exercises, every one of those tasks reads as undispatched — reporting a
// healthy pipeline as broken, which is the original defect inverted, and the
// reason the scoping is a structural part of the count rather than a follow-up.
//
// The test fails if the retryState lookup is removed: the first assertion then
// returns 1 instead of 0.
func TestCountUndispatchedScopesToPublishesThisProcessMade(t *testing.T) {
	ledger := NewDispatchLedger()
	s := &unreviewedScanner{
		dispatchLedger: ledger,
		retryState:     map[string]unreviewedRetry{},
	}
	c := unreviewedCandidate{
		pr:      PullRequest{Owner: "bborbe", Repo: "trading", Number: 480},
		details: PRDetails{HeadSHA: "dfb76b8eff600f0e5f763fb028f0768df75ad2ff"},
	}

	if got := s.countUndispatched([]unreviewedCandidate{c}); got != 0 {
		t.Fatalf("countUndispatched = %d; want 0 for a publish this process did not make", got)
	}

	// The same candidate, now published here and still not dispatched.
	taskID := DeriveTaskID(c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA).String()
	s.retryState[taskID] = unreviewedRetry{attempts: 1}
	if got := s.countUndispatched([]unreviewedCandidate{c}); got != 1 {
		t.Fatalf("countUndispatched = %d; want 1 for a publish this process made", got)
	}

	// Once the consumer observes the spawn, it is no longer undispatched.
	ledger.Mark(taskID)
	if got := s.countUndispatched([]unreviewedCandidate{c}); got != 0 {
		t.Fatalf("countUndispatched = %d; want 0 once the dispatch was observed", got)
	}

	// A stale re-drive publishes under a SALTED id, and the task event — and so
	// the mark — carries that id. Counting against the deterministic id alone
	// would leave a recovered task reading as undispatched forever, which is the
	// false signal this instrument exists to remove.
	redriveID := "salted-redrive-id"
	s.retryState[taskID] = unreviewedRetry{attempts: 3, publishedID: redriveID}
	if got := s.countUndispatched([]unreviewedCandidate{c}); got != 1 {
		t.Fatalf("countUndispatched = %d; want 1 while the salted re-drive is unmarked", got)
	}
	ledger.Mark(redriveID)
	if got := s.countUndispatched([]unreviewedCandidate{c}); got != 0 {
		t.Fatalf("countUndispatched = %d; want 0 once the salted re-drive dispatched", got)
	}
}
