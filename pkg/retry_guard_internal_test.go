// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("retryGuardOutcomeToken", func() {
	DescribeTable(
		"derives the clear-line token from the diagnostics outcome",
		func(diagnosticsOutcome string, status string, want string) {
			Expect(retryGuardOutcomeToken(diagnosticsOutcome, status)).To(Equal(want))
		},
		Entry("failed diagnostics stays failed", "failed", "completed", "failed"),
		Entry("success diagnostics reads completed", "success", "completed", "completed"),
		Entry("skipped diagnostics stays skipped", "skipped", "completed", "skipped"),
		Entry("absent diagnostics falls back to aborted", "", "aborted", "aborted"),
		Entry("absent diagnostics falls back to completed", "", "completed", "completed"),
		// An unknown but non-empty outcome is the producer's own token and is used
		// verbatim. The budget-expiry path is why: the agent controller writes
		// `run_outcome: budget_expired` while the status stays in_progress, so
		// falling back to the status would label a finished run as still running —
		// the exact confusion this clear path exists to remove.
		Entry(
			"budget_expired outcome is used verbatim",
			"budget_expired",
			"in_progress",
			"budget_expired",
		),
		Entry(
			"any other non-empty outcome is used verbatim",
			"unexpected",
			"aborted",
			"unexpected",
		),
	)
})

var _ = Describe("splitRetryGuardKey", func() {
	DescribeTable("splits a ledger key into pull request and head SHA",
		func(key string, wantPR string, wantSHA string) {
			pr, sha := splitRetryGuardKey(key)
			Expect(pr).To(Equal(wantPR))
			Expect(sha).To(Equal(wantSHA))
		},
		Entry("key with a head SHA", "bborbe/repo#42@abc123", "bborbe/repo#42", "abc123"),
		Entry("key without a head SHA", "bborbe/repo#42", "bborbe/repo#42", ""),
	)
})
