// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

import libtime "github.com/bborbe/time"

// NewAgeFilter returns a filter that skips PRs whose UpdatedAt is older
// than (referenceTime - maxAge). A zero maxAge disables the filter.
//
// referenceTime is the watcher's startTime — captured once at process
// start and threaded through factory.CreateWatcher. This avoids any
// CurrentDateTimeGetter injection: a watcher pod's lifetime is bounded
// by its restart cadence (hours), and the age filter's granularity is
// days, so sub-day clock drift is irrelevant.
func NewAgeFilter(maxAge libtime.Duration, referenceTime libtime.DateTime) TaskCreationFilter {
	return &ageFilter{
		maxAge:        maxAge,
		referenceTime: referenceTime,
	}
}

type ageFilter struct {
	maxAge        libtime.Duration
	referenceTime libtime.DateTime
}

func (f *ageFilter) Skip(pr PR) bool {
	if f.maxAge <= 0 {
		return false
	}
	cutoff := f.referenceTime.Add(-f.maxAge)
	return pr.UpdatedAt.Before(cutoff)
}
