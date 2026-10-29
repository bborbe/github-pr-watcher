// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package coalesce_test

import (
	"context"
	"strconv"
	stdtime "time"

	"github.com/bborbe/errors"
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

// fireEvenIfStopped runs every timer that has not fired, including one whose
// Stop arrived after the timer had already begun running. That race is what the
// coalescer's generation counter exists to survive.
func (s *manualScheduler) fireEvenIfStopped() {
	for _, t := range s.timers {
		if t.fired {
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
		now         stdtime.Time
		scheduler   *manualScheduler
		dispatched  []string
		dispatchErr error
		gate        coalesce.Coalescer
		key         coalesce.Key
	)

	advance := func(d stdtime.Duration) {
		now = now.Add(d)
	}
	clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTime(now)
	})
	dispatch := func(_ context.Context, url string) error {
		if dispatchErr != nil {
			return dispatchErr
		}
		dispatched = append(dispatched, url)
		return nil
	}
	const url = "https://github.com/bborbe/nuke/pull/815"

	// push mirrors the webhook path: a true result means the caller publishes
	// and then confirms with MarkEmitted.
	push := func(headSHA string) bool {
		leading := gate.Push(key, headSHA, url, libtime.DateTime(now))
		if leading {
			Expect(dispatch(context.Background(), url)).To(Succeed())
			gate.MarkEmitted(key, headSHA, libtime.DateTime(now))
		}
		return leading
	}
	// admit mirrors the poll path.
	admit := func(headSHA string, pushAt stdtime.Time) coalesce.Decision {
		return gate.Admit(
			key,
			headSHA,
			libtime.DateTime(pushAt),
			libtime.DateTime(now),
		)
	}

	BeforeEach(func() {
		now = stdtime.Date(2026, 10, 27, 12, 0, 0, 0, stdtime.UTC)
		scheduler = &manualScheduler{}
		dispatched = nil
		dispatchErr = nil
		gate = coalesce.NewCoalescer(context.Background(), hold, scheduler, dispatch, clock)
		key = coalesce.Key{Owner: "bborbe", Repo: "nuke", Number: 815}
	})

	AfterEach(func() {
		gate.Close()
	})

	Describe("Push", func() {
		It("dispatches the first push of a burst immediately", func() {
			Expect(push("head-a")).To(BeTrue())
			Expect(dispatched).To(HaveLen(1))
			Expect(scheduler.live()).To(Equal(0))
		})

		It(
			"collapses six pushes in seven minutes into one leading and one trailing dispatch",
			func() {
				Expect(push("head-1")).To(BeTrue())
				for i := 1; i <= 6; i++ {
					advance(stdtime.Minute)
					Expect(push("head-" + strconv.Itoa(i+1))).To(BeFalse())
				}
				Expect(dispatched).To(HaveLen(1))
				Expect(scheduler.live()).To(Equal(1))

				scheduler.fireAll()

				Expect(dispatched).To(HaveLen(2))
				Expect(dispatched[1]).To(Equal(url))
			},
		)

		It("still yields two dispatches when the burst outlasts the hold window", func() {
			Expect(push("head-0")).To(BeTrue())
			for i := 1; i <= 20; i++ {
				advance(stdtime.Minute)
				Expect(push("head-" + strconv.Itoa(i))).To(BeFalse())
			}

			scheduler.fireAll()

			Expect(dispatched).To(HaveLen(2))
		})

		It("dispatches a lone push on a quiet PR with no added latency", func() {
			Expect(push("head-a")).To(BeTrue())

			advance(30 * stdtime.Minute)
			Expect(push("head-b")).To(BeTrue())
			Expect(dispatched).To(HaveLen(2))
		})

		It("ignores a repeated delivery of a head it already published", func() {
			Expect(push("head-a")).To(BeTrue())

			advance(stdtime.Minute)
			Expect(push("head-a")).To(BeFalse())
			Expect(scheduler.live()).To(Equal(0))
		})

		It("re-arms the trailing dispatch when the publish fails", func() {
			Expect(push("head-a")).To(BeTrue())
			advance(stdtime.Minute)
			Expect(push("head-b")).To(BeFalse())

			dispatchErr = errors.Errorf(context.Background(), "kafka down")
			scheduler.fireAll()

			// The failed head must stay claimable: a timer is armed again and
			// the head is not recorded as published.
			Expect(scheduler.live()).To(Equal(1))
			Expect(admit("head-b", now.Add(-10*stdtime.Minute))).To(Equal(coalesce.Hold))

			dispatchErr = nil
			advance(hold)
			scheduler.fireAll()
			Expect(dispatched).To(HaveLen(2))
			Expect(dispatched[1]).To(Equal(url))
		})

		It("ignores a timer that was superseded while it was firing", func() {
			Expect(push("head-a")).To(BeTrue())
			advance(stdtime.Minute)
			Expect(push("head-b")).To(BeFalse())
			advance(stdtime.Minute)
			Expect(push("head-c")).To(BeFalse())

			// Both timers run, as if the first had already begun when Stop
			// arrived. Only the current generation may publish.
			scheduler.fireEvenIfStopped()

			Expect(dispatched).To(HaveLen(2))
		})
	})

	Describe("Admit", func() {
		It("emits the first head it sees, so a poll-only deployment is unchanged", func() {
			Expect(admit("head-a", now)).To(Equal(coalesce.Emit))
		})

		It("settles a head that was already published, so the cursor advances", func() {
			Expect(push("head-a")).To(BeTrue())

			Expect(admit("head-a", now)).To(Equal(coalesce.Settled))
		})

		It("holds a head the PR was still pushing when the poll ran", func() {
			Expect(push("head-a")).To(BeTrue())

			advance(stdtime.Minute)
			Expect(admit("head-b", now)).To(Equal(coalesce.Hold))
		})

		It("holds a head while a trailing dispatch is scheduled", func() {
			Expect(push("head-a")).To(BeTrue())
			advance(stdtime.Minute)
			Expect(push("head-b")).To(BeFalse())

			advance(10 * stdtime.Minute)
			Expect(admit("head-b", now.Add(-10*stdtime.Minute))).To(Equal(coalesce.Hold))
		})

		It("emits a quiet head once no trailing dispatch is outstanding", func() {
			Expect(admit("head-a", now.Add(-30*stdtime.Minute))).To(Equal(coalesce.Emit))
		})

		It("keeps holding when a poll carries a stale updated_at mid-burst", func() {
			Expect(push("head-a")).To(BeTrue())
			advance(stdtime.Minute)
			Expect(push("head-b")).To(BeFalse())
			scheduler.fireAll()
			advance(stdtime.Minute)

			// A poll whose updated_at predates the webhook's last push must not
			// make the PR look quiet enough for a fresh leading edge.
			Expect(admit("head-c", now.Add(-30*stdtime.Minute))).To(Equal(coalesce.Hold))
		})
	})

	Describe("Close", func() {
		It("stops every timer even when the process context is already cancelled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			stopping := coalesce.NewCoalescer(ctx, hold, scheduler, dispatch, clock)
			Expect(stopping.Push(key, "head-a", url, libtime.DateTime(now))).To(BeTrue())
			stopping.MarkEmitted(key, "head-a", libtime.DateTime(now))
			advance(stdtime.Minute)
			Expect(stopping.Push(key, "head-b", url, libtime.DateTime(now))).To(BeFalse())
			Expect(scheduler.live()).To(Equal(1))

			stopping.Close()

			Expect(scheduler.live()).To(Equal(0))
		})
	})
})
