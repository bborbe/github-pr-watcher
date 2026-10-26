// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RetryGuard", func() {
	const (
		owner   = "bborbe"
		repo    = "repo"
		headSHA = "abc123"
	)

	var (
		ctx             context.Context
		db              libkv.DB
		ghClient        *mocks.GitHubClient
		guard           pkg.RetryGuard
		now             libtime.DateTime
		reviewerLogins  []string
		pr              pkg.RetryGuardPR
		otherPR         pkg.RetryGuardPR
		check           func(pkg.RetryGuardPR) (pkg.RetryGuardDecision, error)
		record          func(pkg.RetryGuardPR, string) error
		inflightCounter func() float64
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = pkg.NewMemDB()
		ghClient = new(mocks.GitHubClient)
		now = libtime.DateTime(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
		reviewerLogins = []string{"octopus-pr-reviewer"}
		guard = pkg.NewRetryGuard(
			ghClient,
			pkg.NewMetrics(),
			reviewerLogins,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now }),
		)
		pr = pkg.RetryGuardPR{Owner: owner, Repo: repo, Number: 42, HeadSHA: headSHA}
		otherPR = pkg.RetryGuardPR{Owner: owner, Repo: repo, Number: 42, HeadSHA: "def456"}
		inflightCounter = func() float64 { return readPRPublishedCounter("inflight_skipped") }

		check = func(target pkg.RetryGuardPR) (pkg.RetryGuardDecision, error) {
			var (
				decision pkg.RetryGuardDecision
				checkErr error
			)
			err := db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
				decision, checkErr = guard.Check(ctx, tx, target)
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
			return decision, checkErr
		}

		record = func(target pkg.RetryGuardPR, taskID string) error {
			return db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
				return guard.Record(ctx, tx, target, taskID)
			})
		}
	})

	It("AC1: suppresses a fresh in-flight record with no verdict", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		before := inflightCounter()
		decision, err := check(pr)
		after := inflightCounter()

		Expect(decision.Action).To(Equal(pkg.RetryGuardActionSkip))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
		Expect(decision.ExistingTaskID).To(Equal("task-1"))
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(after - before).To(Equal(1.0))
	})

	It("AC2: allows a re-run when a verdict exists at the head SHA", func() {
		ghClient.ListReviewsReturns([]pkg.Review{{
			AuthorLogin: "OCTOPUS-PR-REVIEWER[BOT]",
			CommitID:    headSHA,
		}}, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		before := inflightCounter()
		decision, err := check(pr)
		after := inflightCounter()

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreateForceOverrodeVerdict))
		Expect(after - before).To(Equal(0.0))
	})

	It("AC3: publishes with no record and does not look up verdicts", func() {
		decision, err := check(pr)

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreate))
		Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
	})

	It("AC4: ignores a record older than the staleness bound", func() {
		Expect(record(pr, "task-1")).To(Succeed())
		now = now.Add(libtime.Duration(61 * time.Minute))

		decision, err := check(pr)

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreateStaleRecordIgnored))
		Expect(decision.RecordAge.Duration()).To(BeNumerically(">=", 60*time.Minute))
		Expect(decision.RecordAge.Duration()).To(BeNumerically("<=", 62*time.Minute))
	})

	It("AC7: Record after a create makes the next Check skip", func() {
		ghClient.ListReviewsReturns(nil, nil)
		first, err := check(pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Action).To(Equal(pkg.RetryGuardActionCreate))

		Expect(record(pr, "task-1")).To(Succeed())

		second, err := check(pr)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(second.Action).To(Equal(pkg.RetryGuardActionSkip))
		Expect(second.ExistingTaskID).To(Equal("task-1"))
	})

	It("Check writes nothing, so two checks in a row both create", func() {
		first, err := check(pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Action).To(Equal(pkg.RetryGuardActionCreate))

		second, err := check(pr)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(second.Reason).To(Equal(pkg.RetryGuardReasonCreate))
	})

	It("prunes stale records on write, so an old key is gone rather than stale", func() {
		Expect(record(pr, "task-1")).To(Succeed())
		now = now.Add(libtime.Duration(2 * time.Hour))
		Expect(record(otherPR, "task-2")).To(Succeed())

		decision, err := check(pr)

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreate))
	})

	It("clamps a negative age to fresh", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())
		now = now.Add(libtime.Duration(-5 * time.Minute))

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	It("publishes when the verdict lookup fails", func() {
		ghClient.ListReviewsReturns(nil, errors.Errorf(ctx, "github down"))
		Expect(record(pr, "task-1")).To(Succeed())

		before := inflightCounter()
		decision, err := check(pr)
		after := inflightCounter()

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreate))
		Expect(after - before).To(Equal(0.0))
	})

	It("skips when no reviewer logins are configured, even with a review", func() {
		guard = pkg.NewRetryGuard(
			ghClient,
			pkg.NewMetrics(),
			nil,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now }),
		)
		ghClient.ListReviewsReturns([]pkg.Review{{
			AuthorLogin: "octopus-pr-reviewer",
			CommitID:    headSHA,
		}}, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	It("skips when the only verdict is at a different SHA", func() {
		ghClient.ListReviewsReturns([]pkg.Review{{
			AuthorLogin: "octopus-pr-reviewer",
			CommitID:    "other-sha",
		}}, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	It("keys records per head SHA", func() {
		Expect(record(pr, "task-1")).To(Succeed())

		decision, err := check(otherPR)

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreate))
	})
})
