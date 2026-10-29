// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package coalesce_test

import (
	"context"
	"strconv"
	stdtime "time"

	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// manualScheduler stands in for time.AfterFunc so the hold window is asserted
// on a fake clock instead of a real sleep.
type manualScheduler struct {
	timers []*manualTimer
}

func (s *manualScheduler) AfterFunc(d stdtime.Duration, f func()) coalesce.Timer {
	t := &manualTimer{duration: d, run: f}
	s.timers = append(s.timers, t)
	return t
}

// fireAll runs every timer that was neither stopped nor already fired.
func (s *manualScheduler) fireAll() {
	for _, t := range s.timers {
		if t.stopped || t.fired {
			continue
		}
		t.fired = true
		t.run()
	}
}

// live counts timers still waiting to fire.
func (s *manualScheduler) live() int {
	count := 0
	for _, t := range s.timers {
		if !t.stopped && !t.fired {
			count++
		}
	}
	return count
}

type manualTimer struct {
	duration stdtime.Duration
	run      func()
	stopped  bool
	fired    bool
}

func (t *manualTimer) Stop() { t.stopped = true }

var _ = Describe("Coalescer", func() {
	const hold = 3 * stdtime.Minute

	var (
		now        stdtime.Time
		scheduler  *manualScheduler
		dispatched []string
		gate       coalesce.Coalescer
		key        coalesce.Key
	)

	advance := func(d stdtime.Duration) {
		now = now.Add(d)
	}
	clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTime(now)
	})
	dispatch := func(_ context.Context, url string) error {
		dispatched = append(dispatched, url)
		return nil
	}
	push := func(headSHA string, at stdtime.Time) bool {
		return gate.Push(
			key,
			headSHA,
			"https://github.com/bborbe/nuke/pull/815",
			libtime.DateTime(at),
		)
	}
	admit := func(headSHA string, pushAt, at stdtime.Time) bool {
		return gate.Admit(
			key,
			headSHA,
			libtime.DateTime(pushAt),
			libtime.DateTime(at),
		)
	}

	BeforeEach(func() {
		now = stdtime.Date(2026, 10, 27, 12, 0, 0, 0, stdtime.UTC)
		scheduler = &manualScheduler{}
		dispatched = nil
		gate = coalesce.NewCoalescer(context.Background(), hold, scheduler, dispatch, clock)
		key = coalesce.Key{Owner: "bborbe", Repo: "nuke", Number: 815}
	})

	AfterEach(func() {
		gate.Close()
	})

	Describe("Push", func() {
		It("dispatches the first push of a burst immediately", func() {
			Expect(push("head-a", now)).To(BeTrue())
			Expect(dispatched).To(BeEmpty())
			Expect(scheduler.live()).To(Equal(0))
		})

		It(
			"collapses six pushes in seven minutes into one leading and one trailing dispatch",
			func() {
				Expect(push("head-1", now)).To(BeTrue())
				for i := 1; i <= 6; i++ {
					advance(stdtime.Minute)
					Expect(push("head-"+itoa(i+1), now)).To(BeFalse())
				}
				Expect(dispatched).To(BeEmpty())
				Expect(scheduler.live()).To(Equal(1))

				scheduler.fireAll()

				Expect(dispatched).To(HaveLen(1))
				Expect(dispatched[0]).To(Equal("https://github.com/bborbe/nuke/pull/815"))
			},
		)

		It("still yields two dispatches when the burst outlasts the hold window", func() {
			Expect(push("head-0", now)).To(BeTrue())
			for i := 1; i <= 20; i++ {
				advance(stdtime.Minute)
				Expect(push("head-"+itoa(i), now)).To(BeFalse())
			}

			scheduler.fireAll()

			Expect(dispatched).To(HaveLen(1))
		})

		It("dispatches a lone push on a quiet PR with no added latency", func() {
			Expect(push("head-a", now)).To(BeTrue())

			advance(30 * stdtime.Minute)
			Expect(push("head-b", now)).To(BeTrue())
			Expect(scheduler.live()).To(Equal(0))
		})

		It("ignores a repeated delivery of the head it already emitted", func() {
			Expect(push("head-a", now)).To(BeTrue())

			advance(stdtime.Minute)
			Expect(push("head-a", now)).To(BeFalse())
			Expect(scheduler.live()).To(Equal(0))
		})
	})

	Describe("Admit", func() {
		It("admits the first head it sees, so a poll-only deployment is unchanged", func() {
			Expect(admit("head-a", now, now)).To(BeTrue())
		})

		It("refuses a head the PR was still pushing when the poll ran", func() {
			Expect(push("head-a", now)).To(BeTrue())

			advance(stdtime.Minute)
			Expect(admit("head-b", now, now)).To(BeFalse())
		})

		It("refuses a head while a trailing dispatch is scheduled", func() {
			Expect(push("head-a", now)).To(BeTrue())
			advance(stdtime.Minute)
			Expect(push("head-b", now)).To(BeFalse())

			advance(10 * stdtime.Minute)
			Expect(admit("head-b", now.Add(-10*stdtime.Minute), now)).To(BeFalse())
		})

		It("admits a quiet head once no trailing dispatch is outstanding", func() {
			Expect(admit("head-a", now.Add(-30*stdtime.Minute), now)).To(BeTrue())
		})

		It("refuses a second record for a head it already emitted", func() {
			Expect(admit("head-a", now.Add(-30*stdtime.Minute), now)).To(BeTrue())

			advance(30 * stdtime.Minute)
			Expect(admit("head-a", now.Add(-60*stdtime.Minute), now)).To(BeFalse())
		})
	})
})

// itoa keeps the table indices readable without pulling strconv into every
// closure.
func itoa(i int) string {
	return strconv.Itoa(i)
}
