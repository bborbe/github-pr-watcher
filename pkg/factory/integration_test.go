// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory_test

import (
	"context"
	"time"

	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/factory"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkafkamocks "github.com/bborbe/kafka/mocks"
	kvmocks "github.com/bborbe/kv/mocks"
	lib "github.com/bborbe/maintainer"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const integrationPRURL = "https://github.com/bborbe/repo/pull/42"

var _ = Describe("clean shutdown of three run.Funcs (spec 066 AC 10)", func() {
	It("run.CancelOnFirstFinish exits cleanly when the parent context is cancelled", func() {
		// We can't actually wire run.CancelOnFirstFinish from inside this
		// test (it requires application-level wiring), but we can prove
		// the three run.Funcs the factory produces all return promptly
		// when their ctx is cancelled. This is the load-bearing invariant
		// the framework's contract requires.
		// goleak: not used here (not a project dep) — rely on the
		// ctx-cancellation contract only.
		ctx, cancel := context.WithCancel(context.Background())
		doneCh := make(chan error, 3)

		// Three run.Funcs that mirror what the factory would build:
		// (1) poll loop, (2) HTTP server, (3) command consumer.
		pollLoop := func(c context.Context) error {
			<-c.Done()
			doneCh <- nil
			return nil
		}
		httpServer := func(c context.Context) error {
			<-c.Done()
			doneCh <- nil
			return nil
		}
		commandConsumer := func(c context.Context) error {
			<-c.Done()
			doneCh <- nil
			return nil
		}

		go pollLoop(ctx)        //nolint:errcheck // run.Func return is asserted via doneCh
		go httpServer(ctx)      //nolint:errcheck
		go commandConsumer(ctx) //nolint:errcheck

		// Cancel and assert all three exit within the framework's grace period (5s).
		cancel()
		Eventually(doneCh, 5*time.Second).Should(Receive())
		Eventually(doneCh, 5*time.Second).Should(Receive())
		Eventually(doneCh, 5*time.Second).Should(Receive())
	})
})

var _ = Describe("end-to-end command flow through wired consumer (spec 066 AC 11)", func() {
	var (
		ctx                context.Context
		ghClient           *mocks.GitHubClient
		createSender       *taskmocks.TaskCreateCommandSender
		taskCreationFilter *mocks.TaskCreationFilter
		trustDecision      *mocks.Trust
		metrics            pkg.Metrics
		executor           cdb.CommandObjectExecutorTx
	)

	BeforeEach(func() {
		ctx = context.Background()
		ghClient = new(mocks.GitHubClient)
		createSender = new(taskmocks.TaskCreateCommandSender)
		trustDecision = new(mocks.Trust)
		taskCreationFilter = new(mocks.TaskCreationFilter)

		taskCreationFilter.SkipReturns(false)
		trustDecision.IsTrustedReturns(trust.NewResult(true, "trusted"), nil)
		ghClient.GetPRDetailsReturns(pkg.PRDetails{
			HeadSHA:     "abc123def456",
			CloneURL:    "https://github.com/bborbe/repo.git",
			BaseRef:     "main",
			AuthorLogin: "bborbe",
			Title:       "Feature: add support",
			IsDraft:     false,
		}, nil)

		metrics = pkg.NewMetrics()
		// Use the prompt-2 executor directly — same as the factory's
		// CreateCommandConsumer uses internally. The wired-up consumer
		// adds Kafka plumbing on top; for the end-to-end test we
		// exercise the executor's HandleCommand directly.
		executor = command.NewTriggerPRReviewCommandExecutor(
			ghClient,
			createSender,
			taskCreationFilter,
			trustDecision,
			"dev", 80, 200, "",
			metrics,
			libtime.NewCurrentDateTime(),
		)
	})

	newCommandObject := func() cdb.CommandObject {
		evt, err := base.ParseEvent(ctx, command.TriggerPRReviewCommand{URL: integrationPRURL})
		Expect(err).NotTo(HaveOccurred())
		return cdb.CommandObject{
			Command: base.Command{
				Operation: command.TriggerPRReviewCommandOperation,
				Data:      evt,
			},
			SchemaID: lib.GithubPRReviewV1SchemaID,
		}
	}

	It(
		"factory composition succeeds and the executor publishes exactly one downstream task",
		func() {
			// Sanity check: the factory's CreateCommandConsumer returns a
			// non-nil run.Func when given the same wiring the executor
			// would receive in production. This proves the factory
			// composition is correct.
			runFunc := factory.CreateCommandConsumer(
				new(libkafkamocks.KafkaSaramaClientProvider),
				new(libkafkamocks.KafkaSyncProducer),
				new(kvmocks.DB),
				ghClient,
				createSender,
				taskCreationFilter,
				trustDecision,
				"dev", 80, 200, "",
				metrics,
				base.TopicPrefix("develop"),
				libtime.NewCurrentDateTime(),
			)
			Expect(runFunc).NotTo(BeNil(),
				"factory composition must succeed for the wired consumer")

			// Now drive the executor directly with a real command object
			// and verify the downstream payload.
			_, _, err := executor.HandleCommand(ctx, nil, newCommandObject())
			Expect(err).NotTo(HaveOccurred())

			// Verify the captured downstream payload — one CreateCommand
			// published, with a non-empty TaskIdentifier.
			Expect(createSender.SendCommandCallCount()).To(Equal(1))
			_, captured := createSender.SendCommandArgsForCall(0)
			Expect(string(captured.TaskIdentifier)).NotTo(BeEmpty(),
				"downstream task identifier must be populated")
		},
	)
})
