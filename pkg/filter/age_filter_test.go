// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"time"

	"github.com/bborbe/github-pr-watcher/pkg/filter"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgeFilter", func() {
	var (
		referenceTime libtime.DateTime
		maxAge        libtime.Duration
	)

	BeforeEach(func() {
		// fixed reference time: 2026-05-02T12:00:00Z
		referenceTime = libtime.DateTime(time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC))
		maxAge = libtime.Duration(90 * 24 * time.Hour) // 90 days
	})

	Context("maxAge = 0 (filter disabled)", func() {
		It("never skips regardless of UpdatedAt", func() {
			f := filter.NewAgeFilter(0, referenceTime)
			// very old PR
			old := libtime.DateTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(f.Skip(filter.PR{UpdatedAt: old})).To(BeFalse())
		})
	})

	Context("maxAge = 90 days", func() {
		It("skips a PR updated 100 days ago", func() {
			f := filter.NewAgeFilter(maxAge, referenceTime)
			updatedAt := libtime.DateTime(
				time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC).Add(-100 * 24 * time.Hour),
			)
			Expect(f.Skip(filter.PR{UpdatedAt: updatedAt})).To(BeTrue())
		})
		It("does not skip a PR updated 89 days ago", func() {
			f := filter.NewAgeFilter(maxAge, referenceTime)
			updatedAt := libtime.DateTime(
				time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC).Add(-89 * 24 * time.Hour),
			)
			Expect(f.Skip(filter.PR{UpdatedAt: updatedAt})).To(BeFalse())
		})
		It(
			"does not skip a PR at the cutoff boundary (exactly 90 days ago — not strictly before)",
			func() {
				f := filter.NewAgeFilter(maxAge, referenceTime)
				// cutoff = referenceTime - 90d; PR updated exactly at cutoff is NOT before cutoff
				updatedAt := libtime.DateTime(
					time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC).Add(-90 * 24 * time.Hour),
				)
				Expect(f.Skip(filter.PR{UpdatedAt: updatedAt})).To(BeFalse())
			},
		)
		It("does not skip a PR updated today", func() {
			f := filter.NewAgeFilter(maxAge, referenceTime)
			Expect(f.Skip(filter.PR{UpdatedAt: referenceTime})).To(BeFalse())
		})
	})

	Context("negative maxAge (should not be created, but guard: treated as disabled)", func() {
		It("never skips when maxAge is negative", func() {
			f := filter.NewAgeFilter(libtime.Duration(-1*24*time.Hour), referenceTime)
			old := libtime.DateTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
			Expect(f.Skip(filter.PR{UpdatedAt: old})).To(BeFalse())
		})
	})
})
