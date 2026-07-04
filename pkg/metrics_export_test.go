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
