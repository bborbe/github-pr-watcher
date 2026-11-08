// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package escalate_test

import (
	"context"
	"errors"
	"sync"

	"github.com/bborbe/github-pr-watcher/pkg/escalate"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func escalation(class escalate.Class) escalate.Escalation {
	return escalate.Escalation{
		PRURL:   "https://github.com/bborbe/example/pull/7",
		Repo:    "bborbe/example",
		Number:  7,
		HeadSHA: "abc123",
		Class:   class,
		Detail:  "test detail",
	}
}

var _ = Describe("Escalator", func() {
	var (
		ctx       context.Context
		published []string
		failWith  error
		escalator escalate.Escalator
	)

	BeforeEach(func() {
		ctx = context.Background()
		published = nil
		failWith = nil
		escalator = escalate.NewEscalator(
			func(ctx context.Context, message string) error {
				if failWith != nil {
					return failWith
				}
				published = append(published, message)
				return nil
			},
		)
	})

	Describe("Escalate", func() {
		It("publishes once per (PR, head SHA, class)", func() {
			first, err := escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).To(BeNil())
			Expect(first).To(BeTrue())

			second, err := escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).To(BeNil())
			Expect(second).To(BeFalse())

			Expect(published).To(HaveLen(1))
		})

		It("publishes again when the head SHA moves", func() {
			_, err := escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).To(BeNil())

			moved := escalation(escalate.ClassUntrustedAuthor)
			moved.HeadSHA = "def456"
			again, err := escalator.Escalate(ctx, moved)
			Expect(err).To(BeNil())
			Expect(again).To(BeTrue())
			Expect(published).To(HaveLen(2))
		})

		It("publishes once per class for the same PR and head SHA", func() {
			_, err := escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).To(BeNil())
			_, err = escalator.Escalate(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(published).To(HaveLen(2))
		})

		It("does not mark the episode notified when the publish fails", func() {
			failWith = errors.New("kafka unavailable")

			ok, err := escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).NotTo(BeNil())
			Expect(ok).To(BeFalse())
			Expect(published).To(BeEmpty())

			failWith = nil
			ok, err = escalator.Escalate(ctx, escalation(escalate.ClassUntrustedAuthor))
			Expect(err).To(BeNil())
			Expect(ok).To(BeTrue())
			Expect(published).To(HaveLen(1))
		})

		It("publishes exactly once under concurrent callers", func() {
			var wg sync.WaitGroup
			results := make([]bool, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					ok, err := escalator.Escalate(
						ctx,
						escalation(escalate.ClassUntrustedAuthor),
					)
					if err != nil {
						return
					}
					results[index] = ok
				}(i)
			}
			wg.Wait()
			Expect(results).To(ContainElement(true))
			Expect(results).To(ContainElement(false))
			Expect(published).To(HaveLen(1))
		})

		It("carries the PR URL and the class in the message", func() {
			_, err := escalator.Escalate(ctx, escalation(escalate.ClassReviewRequestChanges))
			Expect(err).To(BeNil())
			Expect(published).To(HaveLen(1))
			Expect(published[0]).To(ContainSubstring("https://github.com/bborbe/example/pull/7"))
			Expect(published[0]).To(ContainSubstring("class=review-request-changes"))
		})
	})

	Describe("ObserveNotGreen", func() {
		It("waits for the pinned threshold before escalating", func() {
			for i := 1; i < escalate.ChecksNotGreenThreshold; i++ {
				ok, err := escalator.ObserveNotGreen(
					ctx,
					escalation(escalate.ClassChecksNotGreen),
				)
				Expect(err).To(BeNil())
				Expect(ok).To(BeFalse())
			}
			Expect(published).To(BeEmpty())

			ok, err := escalator.ObserveNotGreen(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeTrue())
			Expect(published).To(HaveLen(1))

			// Further observations stay deduped: one item per episode.
			ok, err = escalator.ObserveNotGreen(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeFalse())
			Expect(published).To(HaveLen(1))
		})

		It("does not clear the notified episode on Reset", func() {
			ok, err := escalator.Escalate(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeTrue())

			escalator.Reset(ctx, "bborbe/example", 7)

			ok, err = escalator.Escalate(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeFalse())
			Expect(published).To(HaveLen(1))
		})

		It("clears the counter for every head SHA of the PR", func() {
			first := escalation(escalate.ClassChecksNotGreen)
			second := escalation(escalate.ClassChecksNotGreen)
			second.HeadSHA = "def456"

			_, err := escalator.ObserveNotGreen(ctx, first)
			Expect(err).To(BeNil())
			_, err = escalator.ObserveNotGreen(ctx, second)
			Expect(err).To(BeNil())

			escalator.Reset(ctx, "bborbe/example", 7)

			okFirst, err := escalator.ObserveNotGreen(ctx, first)
			Expect(err).To(BeNil())
			Expect(okFirst).To(BeFalse())
			okSecond, err := escalator.ObserveNotGreen(ctx, second)
			Expect(err).To(BeNil())
			Expect(okSecond).To(BeFalse())
			Expect(published).To(BeEmpty())
		})

		It("restarts the counter after Reset", func() {
			ok, err := escalator.ObserveNotGreen(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeFalse())

			escalator.Reset(ctx, "bborbe/example", 7)

			ok, err = escalator.ObserveNotGreen(ctx, escalation(escalate.ClassChecksNotGreen))
			Expect(err).To(BeNil())
			Expect(ok).To(BeFalse())
			Expect(published).To(BeEmpty())
		})
	})
})
