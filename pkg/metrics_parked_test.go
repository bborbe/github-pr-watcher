// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"strings"

	"github.com/bborbe/github-pr-watcher/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const parkedMetricName = "github_pr_watcher_parked_prs"

var _ = Describe("parked PR metrics", func() {
	It("tracks the parked PR set and its age in seconds", func() {
		metrics := pkg.NewMetrics()

		metrics.SetParkedPRs([]pkg.ParkedPR{
			{Repo: "bborbe/repo", Number: 42, AgeSeconds: 3600},
		})

		expected := `# HELP github_pr_watcher_parked_prs Seconds each open, trusted-author, oversized pull request has been continuously parked at human_review, by repo and PR number. Seeded from the PR's updated-at on the first scan that observes it parked and held in a ledger until its head SHA changes; a complete scan replaces the whole set, so a park that ends disappears within one scan. Untrusted-author parks are not covered. Single replica only.
# TYPE github_pr_watcher_parked_prs gauge
github_pr_watcher_parked_prs{pr="42",repo="bborbe/repo"} 3600
`
		Expect(testutil.GatherAndCompare(
			prometheus.DefaultGatherer,
			strings.NewReader(expected),
			parkedMetricName,
		)).To(Succeed())

		// A full replacement: two series, then one, then none.
		metrics.SetParkedPRs([]pkg.ParkedPR{
			{Repo: "bborbe/repo", Number: 42, AgeSeconds: 3600},
			{Repo: "bborbe/other", Number: 7, AgeSeconds: 60},
		})
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, parkedMetricName,
		)).To(Equal(2))

		metrics.SetParkedPRs([]pkg.ParkedPR{
			{Repo: "bborbe/other", Number: 7, AgeSeconds: 60},
		})
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, parkedMetricName,
		)).To(Equal(1))

		// Leave the shared registry clean for later specs.
		metrics.SetParkedPRs(nil)
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, parkedMetricName,
		)).To(Equal(0))
	})
})
