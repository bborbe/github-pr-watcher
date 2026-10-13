// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"strings"
	"time"

	"github.com/bborbe/github-pr-watcher/pkg"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const unreviewedMetricName = "github_pr_watcher_unreviewed_prs"

var _ = Describe("unreviewed PR metrics", func() {
	It("tracks the unreviewed PR set and the last-scan timestamp", func() {
		metrics := pkg.NewMetrics()
		scannedAt := libtime.DateTime(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))

		metrics.SetUnreviewedPRs([]pkg.UnreviewedPR{
			{Repo: "bborbe/repo", Number: 42},
		}, scannedAt)

		// GatherAndCompare proves the label names/values and the value 1.
		expected := `# HELP github_pr_watcher_unreviewed_prs Open pull requests the watcher would hand to the reviewer whose current head SHA has no review from a configured reviewer bot after waiting longer than the unreviewed threshold. Value 1 per such PR.
# TYPE github_pr_watcher_unreviewed_prs gauge
github_pr_watcher_unreviewed_prs{pr="42",repo="bborbe/repo"} 1
`
		Expect(testutil.GatherAndCompare(
			prometheus.DefaultGatherer,
			strings.NewReader(expected),
			unreviewedMetricName,
		)).To(Succeed())
		Expect(testutil.ToFloat64(pkg.UnreviewedScanLastSuccessForTest())).
			To(Equal(float64(scannedAt.Unix())))

		// A full replacement: two series, then one, then none.
		metrics.SetUnreviewedPRs([]pkg.UnreviewedPR{
			{Repo: "bborbe/repo", Number: 42},
			{Repo: "bborbe/other", Number: 7},
		}, scannedAt)
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, unreviewedMetricName,
		)).To(Equal(2))

		metrics.SetUnreviewedPRs([]pkg.UnreviewedPR{
			{Repo: "bborbe/other", Number: 7},
		}, scannedAt)
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, unreviewedMetricName,
		)).To(Equal(1))

		metrics.SetUnreviewedPRs(nil, scannedAt)
		Expect(testutil.GatherAndCount(
			prometheus.DefaultGatherer, unreviewedMetricName,
		)).To(Equal(0))
	})
})
