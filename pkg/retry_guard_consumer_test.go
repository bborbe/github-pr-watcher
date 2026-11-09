// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/IBM/sarama"
	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	libkafka "github.com/bborbe/kafka"
	libkafkamocks "github.com/bborbe/kafka/mocks"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RetryGuardOutcome", func() {
	const (
		owner   = "bborbe"
		repo    = "repo"
		headSHA = "abc123"
	)

	var (
		ctx      context.Context
		db       libkv.DB
		ghClient *mocks.GitHubClient
		guard    pkg.RetryGuard
		now      libtime.DateTime
		pr       pkg.RetryGuardPR
		check    func(pkg.RetryGuardPR) (pkg.RetryGuardDecision, error)
		record   func(pkg.RetryGuardPR, string) error
		consume  func(libkafka.MessageHandlerTx, *sarama.ConsumerMessage) error
		newTask  func(identifier string, status string, content string) agentlib.Task
	)

	newTaskEventMessage := func(ctx context.Context, task agentlib.Task) *sarama.ConsumerMessage {
		event, err := base.ParseEvent(ctx, task)
		Expect(err).NotTo(HaveOccurred())
		eventObject := cdb.EventObject{
			Event:    event,
			ID:       base.EventID(task.TaskIdentifier),
			SchemaID: agentlib.TaskV1SchemaID,
		}
		value, err := json.Marshal(eventObject.Event)
		Expect(err).NotTo(HaveOccurred())
		return &sarama.ConsumerMessage{Key: eventObject.ID.Bytes(), Value: value}
	}

	BeforeEach(func() {
		ctx = context.Background()
		db = pkg.NewMemDB()
		ghClient = new(mocks.GitHubClient)
		now = libtime.DateTime(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
		guard = pkg.NewRetryGuard(
			ghClient,
			pkg.NewMetrics(),
			[]string{"octopus-pr-reviewer"},
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now }),
		)
		pr = pkg.RetryGuardPR{Owner: owner, Repo: repo, Number: 42, HeadSHA: headSHA}

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

		consume = func(handler libkafka.MessageHandlerTx, msg *sarama.ConsumerMessage) error {
			return db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
				return handler.ConsumeMessage(ctx, tx, msg)
			})
		}

		newTask = func(identifier string, status string, content string) agentlib.Task {
			return agentlib.Task{
				Object: base.Object[base.Identifier]{
					Identifier: base.Identifier(identifier),
					Created:    libtime.DateTime(time.Now()),
					Modified:   libtime.DateTime(time.Now()),
				},
				TaskIdentifier: agentlib.TaskIdentifier(identifier),
				Frontmatter:    agentlib.TaskFrontmatter{"status": status},
				Content:        agentlib.TaskContent(content),
			}
		}
	})

	It("AC1: a completed event clears the record so the next check creates", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		msg := newTaskEventMessage(
			ctx,
			newTask("task-1", "completed", "## Diagnostics\n\n```yaml\noutcome: failed\n```\n"),
		)
		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(guard), msg)).To(Succeed())

		decision, err := check(pr)

		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Action).To(Equal(pkg.RetryGuardActionCreate))
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonCreate))
		Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
	})

	DescribeTable("AC2: clears only for a terminal status",
		func(status domain.TaskStatus, wantCalls int) {
			fake := new(mocks.RetryGuard)
			msg := newTaskEventMessage(ctx, newTask("task-1", status.String(), ""))

			Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(fake), msg)).To(Succeed())

			Expect(fake.ClearByTaskIDCallCount()).To(Equal(wantCalls))
		},
		Entry("completed", domain.TaskStatusCompleted, 1),
		Entry("aborted", domain.TaskStatusAborted, 1),
		Entry("in_progress", domain.TaskStatusInProgress, 0),
		Entry("next", domain.TaskStatusNext, 0),
		Entry("backlog", domain.TaskStatusBacklog, 0),
		Entry("hold", domain.TaskStatusHold, 0),
	)

	It("AC2: an in_progress event leaves the record suppressing", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		msg := newTaskEventMessage(ctx, newTask("task-1", "in_progress", ""))
		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(guard), msg)).To(Succeed())

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	It("AC3: a completed event for a different task identifier is a no-op", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		// The handler always asks the guard to clear the event's own task id;
		// the no-op for an unmatched id lives in the ledger, so the fake must
		// see the call with task-2 while the real guard leaves task-1 alone.
		fake := new(mocks.RetryGuard)
		msg := newTaskEventMessage(ctx, newTask("task-2", "completed", ""))
		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(fake), msg)).To(Succeed())
		Expect(fake.ClearByTaskIDCallCount()).To(Equal(1))
		_, _, clearedTaskID, _ := fake.ClearByTaskIDArgsForCall(0)
		Expect(clearedTaskID).To(Equal("task-2"))

		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(guard), msg)).To(Succeed())

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	DescribeTable("AC4: derives the outcome token from the diagnostics",
		func(status string, content string, wantOutcome string) {
			fake := new(mocks.RetryGuard)
			msg := newTaskEventMessage(ctx, newTask("task-1", status, content))

			Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(fake), msg)).To(Succeed())

			Expect(fake.ClearByTaskIDCallCount()).To(Equal(1))
			_, _, taskID, outcome := fake.ClearByTaskIDArgsForCall(0)
			Expect(taskID).To(Equal(string(msg.Key)))
			Expect(outcome).To(Equal(wantOutcome))
		},
		Entry("failed", "completed",
			"## Diagnostics\n\n```yaml\noutcome: failed\n```\n", "failed"),
		Entry("success", "completed",
			"## Diagnostics\n\n```yaml\noutcome: success\n```\n", "completed"),
		Entry("skipped", "completed",
			"## Diagnostics\n\n```yaml\noutcome: skipped\n```\n", "skipped"),
		Entry("aborted without diagnostics", "aborted", "", "aborted"),
	)

	It("AC5: decodes a real event object back into a task", func() {
		fake := new(mocks.RetryGuard)
		msg := newTaskEventMessage(
			ctx,
			newTask("task-abc", "completed", "## Diagnostics\n\n```yaml\noutcome: success\n```\n"),
		)

		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(fake), msg)).To(Succeed())

		Expect(fake.ClearByTaskIDCallCount()).To(Equal(1))
		_, _, taskID, outcome := fake.ClearByTaskIDArgsForCall(0)
		Expect(taskID).To(Equal("task-abc"))
		Expect(outcome).To(Equal("completed"))
	})

	It("AC7: non-terminal events leave the record suppressing", func() {
		ghClient.ListReviewsReturns(nil, nil)
		Expect(record(pr, "task-1")).To(Succeed())

		for _, status := range []domain.TaskStatus{
			domain.TaskStatusInProgress,
			domain.TaskStatusNext,
			domain.TaskStatusBacklog,
			domain.TaskStatusHold,
		} {
			msg := newTaskEventMessage(ctx, newTask("task-1", status.String(), ""))
			Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(guard), msg)).To(Succeed())
		}

		decision, err := check(pr)

		Expect(err).To(HaveOccurred())
		Expect(decision.Reason).To(Equal(pkg.RetryGuardReasonSkipInflight))
	})

	It("ignores a delete event without error", func() {
		fake := new(mocks.RetryGuard)
		msg := &sarama.ConsumerMessage{Key: []byte("task-1"), Value: nil}

		Expect(consume(pkg.NewRetryGuardOutcomeMessageHandler(fake), msg)).To(Succeed())

		Expect(fake.ClearByTaskIDCallCount()).To(Equal(0))
	})

	It("constructs a non-nil consumer run.Func", func() {
		runFunc := pkg.NewRetryGuardOutcomeConsumer(
			new(libkafkamocks.KafkaSaramaClientProvider),
			pkg.NewMemDB(),
			base.TopicPrefix("develop"),
			new(mocks.RetryGuard),
		)
		Expect(runFunc).NotTo(BeNil())
	})
})
