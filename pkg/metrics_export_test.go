// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/prometheus/client_golang/prometheus"
)

// PRPublishedTotalForTest returns the underlying CounterVec so external
// test packages can read label values via prometheus/testutil.ToFloat64.
// The _test.go suffix keeps this file out of production builds.
func PRPublishedTotalForTest() *prometheus.CounterVec {
	return prPublishedTotal
}

// UnreviewedPRsForTest returns the unreviewed-PR GaugeVec so external test
// packages can read it. Never read a removed series back via WithLabelValues —
// that re-creates it; assert removal via testutil.GatherAndCount instead.
func UnreviewedPRsForTest() *prometheus.GaugeVec {
	return unreviewedPRs
}

// UnreviewedScanLastSuccessForTest returns the last-successful-scan timestamp
// gauge so external test packages can read it via testutil.ToFloat64.
func UnreviewedScanLastSuccessForTest() prometheus.Gauge {
	return unreviewedScanLastSuccess
}

// UnreviewedRetriesTotalForTest returns the unreviewed-retries CounterVec so
// external test packages can read it via prometheus/testutil. Reading the
// accessor bypasses the registry, so a test proving init() registration must
// also gather the DefaultGatherer.
func UnreviewedRetriesTotalForTest() *prometheus.CounterVec {
	return unreviewedRetriesTotal
}
