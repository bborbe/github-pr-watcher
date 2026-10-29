// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	task "github.com/bborbe/agent/command/task"
	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OverrideLedger", func() {
	const (
		owner    = "bborbe"
		repo     = "repo"
		number   = 42
		headSHA  = "aa966e2798cdb4f1b035e24e1cee5b4df3e3ad14"
		otherSHA = "cf36875a00000000000000000000000000000000"
	)

	var (
		ctx         context.Context
		db          libkv.DB
		now         libtime.DateTime
		ledger      pkg.OverrideLedger
		pr          pkg.RetryGuardPR
		otherPR     pkg.RetryGuardPR
		currentTime libtime.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = pkg.NewMemDB()
		now = libtime.DateTime(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
		currentTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })
		ledger = pkg.NewOverrideLedger(db, currentTime)
		pr = pkg.RetryGuardPR{Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}
		otherPR = pkg.RetryGuardPR{Owner: owner, Repo: repo, Number: number, HeadSHA: otherSHA}
	})

	It("suppresses nothing before any override is recorded", func() {
		Expect(ledger.Suppressed(ctx, pr)).To(BeFalse())
	})

	It("suppresses the recorded sha and only that sha", func() {
		ledger.Record(ctx, pr, "task-1")
		Expect(ledger.Suppressed(ctx, pr)).To(BeTrue())
		Expect(ledger.Suppressed(ctx, otherPR)).To(BeFalse())
	})

	It("stops suppressing once the override could no longer land", func() {
		// The bound is what keeps a dead override from stranding its PR: after
		// the window the ordinary review path must take over again.
		ledger.Record(ctx, pr, "task-1")
		Expect(ledger.Suppressed(ctx, pr)).To(BeTrue())

		now = libtime.DateTime(now.Add(libtime.Duration(time.Hour + time.Minute)))
		Expect(ledger.Suppressed(ctx, pr)).To(BeFalse())
	})

	It("treats a nil db as a no-op ledger", func() {
		nilLedger := pkg.NewOverrideLedger(nil, currentTime)
		nilLedger.Record(ctx, pr, "task-1")
		Expect(nilLedger.Suppressed(ctx, pr)).To(BeFalse())
	})
})

var _ = Describe("TaskPublisher override ledger gate", func() {
	const (
		owner   = "bborbe"
		repo    = "repo"
		number  = 42
		headSHA = "aa966e2798cdb4f1b035e24e1cee5b4df3e3ad14"
	)

	var (
		ctx          context.Context
		db           libkv.DB
		now          libtime.DateTime
		ledger       pkg.OverrideLedger
		createSender *taskmocks.TaskCreateCommandSender
		ghClient     *mocks.GitHubClient
		metrics      *mocks.Metrics
		publisher    pkg.TaskPublisher
		pr           pkg.PullRequest
		details      pkg.PRDetails
		currentTime  libtime.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = pkg.NewMemDB()
		now = libtime.DateTime(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
		currentTime = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })
		ledger = pkg.NewOverrideLedger(db, currentTime)
		createSender = new(taskmocks.TaskCreateCommandSender)
		ghClient = new(mocks.GitHubClient)
		metrics = new(mocks.Metrics)
		publisher = pkg.NewTaskPublisher(
			createSender,
			trust.And{trust.NewAuthorAllowlist([]string{"alice"})},
			metrics,
			pkg.TaskConfig{
				Stage:       "prod",
				MaxSlugLen:  pkg.DefaultMaxSlugLen,
				MaxTitleLen: pkg.DefaultMaxTitleLen,
			},
			ghClient,
			pkg.WithOverrideLedger(ledger),
		)
		pr = pkg.PullRequest{
			Owner:       owner,
			Repo:        repo,
			Number:      number,
			Title:       "update go module dependencies",
			AuthorLogin: "alice",
			HTMLURL:     "https://github.com/bborbe/repo/pull/42",
		}
		details = pkg.PRDetails{
			HeadSHA:          headSHA,
			CloneURL:         "https://github.com/bborbe/repo.git",
			BaseRef:          "master",
			HeadRepoFullName: "bborbe/repo",
			BaseRepoFullName: "bborbe/repo",
		}
	})

	It("publishes a review when no override owns the sha", func() {
		createSender.SendCommandReturns(nil)

		Expect(publisher.PublishCreate(ctx, pr, "review-task", details)).To(BeTrue())
		Expect(createSender.SendCommandCallCount()).To(Equal(1))
	})

	It("records the sha when a mechanical override is published", func() {
		createSender.SendCommandReturns(nil)

		Expect(publisher.PublishMechanicalOverride(ctx, pr, "override-task", details)).To(BeTrue())
		Expect(ledger.Suppressed(ctx, pkg.RetryGuardPR{
			Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA,
		})).To(BeTrue())
	})

	It("refuses to publish a review for a sha an override owns", func() {
		// This is the exact failure the ledger exists to stop: a producer that
		// does not own the mechanical decision publishing a review anyway.
		// Observed on bborbe/kafka-topic-reader#26 and bborbe/alert#29.
		createSender.SendCommandReturns(nil)
		Expect(publisher.PublishMechanicalOverride(ctx, pr, "override-task", details)).To(BeTrue())

		before := createSender.SendCommandCallCount()
		Expect(publisher.PublishCreate(ctx, pr, "review-task", details)).To(BeFalse())
		Expect(createSender.SendCommandCallCount()).To(Equal(before))
	})

	It("publishes the review again once the suppression window has passed", func() {
		createSender.SendCommandReturns(nil)
		Expect(publisher.PublishMechanicalOverride(ctx, pr, "override-task", details)).To(BeTrue())

		now = libtime.DateTime(now.Add(libtime.Duration(time.Hour + time.Minute)))

		Expect(publisher.PublishCreate(ctx, pr, "review-task", details)).To(BeTrue())
	})

	It("still publishes a review when no ledger is configured", func() {
		// The pre-ledger behaviour must survive a publisher built without one.
		noLedgerPublisher := pkg.NewTaskPublisher(
			createSender,
			trust.And{trust.NewAuthorAllowlist([]string{"alice"})},
			metrics,
			pkg.TaskConfig{Stage: "prod", MaxSlugLen: pkg.DefaultMaxSlugLen, MaxTitleLen: pkg.DefaultMaxTitleLen},
			ghClient,
		)
		createSender.SendCommandReturns(nil)

		Expect(noLedgerPublisher.PublishCreate(ctx, pr, "review-task", details)).To(BeTrue())
		Expect(createSender.SendCommandCallCount()).To(Equal(1))
	})
})

var _ task.CreateCommandSender = (*taskmocks.TaskCreateCommandSender)(nil)
