// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"time"

	"github.com/bborbe/github-pr-watcher/pkg"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pkg.PublishTracker", func() {
	var (
		now             libtime.DateTime
		currentDateTime libtime.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		now = libtime.DateTime(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
		currentDateTime = libtime.CurrentDateTimeGetterFunc(
			func() libtime.DateTime { return now },
		)
	})

	It("starts empty", func() {
		count, last := pkg.NewPublishTracker(currentDateTime).Snapshot()
		Expect(count).To(Equal(int64(0)))
		Expect(last.IsZero()).To(BeTrue())
	})

	It("counts publishes and remembers the last one", func() {
		tracker := pkg.NewPublishTracker(currentDateTime)
		tracker.Record()
		now = now.Add(libtime.Minute)
		tracker.Record()

		count, last := tracker.Snapshot()
		Expect(count).To(Equal(int64(2)))
		Expect(last).To(Equal(now))
	})

	It("is nil-tolerant, so an unwired tracker changes nothing", func() {
		var tracker *pkg.PublishTracker
		Expect(func() { tracker.Record() }).NotTo(Panic())
		count, last := tracker.Snapshot()
		Expect(count).To(Equal(int64(0)))
		Expect(last.IsZero()).To(BeTrue())
	})
})
