// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("retryGuardOutcomeToken", func() {
	DescribeTable("derives the clear-line token from the diagnostics outcome",
		func(diagnosticsOutcome string, status string, want string) {
			Expect(retryGuardOutcomeToken(diagnosticsOutcome, status)).To(Equal(want))
		},
		Entry("failed diagnostics stays failed", "failed", "completed", "failed"),
		Entry("success diagnostics reads completed", "success", "completed", "completed"),
		Entry("skipped diagnostics stays skipped", "skipped", "completed", "skipped"),
		Entry("absent diagnostics falls back to aborted", "", "aborted", "aborted"),
		Entry("absent diagnostics falls back to completed", "", "completed", "completed"),
		Entry("unexpected diagnostics falls back to status", "unexpected", "aborted", "aborted"),
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
