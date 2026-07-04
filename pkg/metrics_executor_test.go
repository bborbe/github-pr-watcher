// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	lib "github.com/bborbe/maintainer"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testPRURL = "https://github.com/bborbe/repo/pull/42"

// The prPublishedTotal counter is pre-registered in init() and shared
// across the test process. To assert label deltas in tests we read the
// counter before and after each executor invocation.
func readPRPublishedCounter(label string) float64 {
	return testutil.ToFloat64(pkg.PRPublishedTotalForTest().WithLabelValues(label))
}

// newTriggerCommandObject marshals a TriggerPRReviewCommand into a
// cdb.CommandObject suitable for the executor's closure.
func newTriggerCommandObject(cmd command.TriggerPRReviewCommand) cdb.CommandObject {
	evt, err := base.ParseEvent(context.Background(), cmd)
	Expect(err).NotTo(HaveOccurred())
	return cdb.CommandObject{
		Command: base.Command{
			Operation: command.TriggerPRReviewCommandOperation,
			Data:      evt,
		},
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
}

var _ = Describe("github_pr_published metric (spec 066 AC 12)", func() {
	// Note: the prPublishedTotal counter is a process-global. We snapshot
	// each label's value before each It and assert the delta. This avoids
	// cross-test interference from earlier specs that may have incremented
	// the counter.
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
		taskCreationFilter = new(mocks.TaskCreationFilter)
		trustDecision = new(mocks.Trust)

		taskCreationFilter.SkipReturns(false)
		trustDecision.IsTrustedReturns(trust.NewResult(true, "trusted"), nil)
		ghClient.GetPRDetailsReturns(pkg.PRDetails{
			HeadSHA:     "abc123",
			CloneURL:    "https://github.com/bborbe/repo.git",
			BaseRef:     "main",
			AuthorLogin: "bborbe",
			Title:       "Feature: add support",
			IsDraft:     false,
		}, nil)

		metrics = pkg.NewMetrics()
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

	// runExecutor invokes the executor's closure with the standard wiring.
	runExecutor := func() error {
		_, _, err := executor.HandleCommand(
			ctx,
			nil,
			newTriggerCommandObject(command.TriggerPRReviewCommand{URL: testPRURL}),
		)
		return err
	}

	It("increments 'create' for a valid invocation", func() {
		before := readPRPublishedCounter("create")
		Expect(runExecutor()).NotTo(HaveOccurred())
		after := readPRPublishedCounter("create")
		Expect(after-before).To(Equal(1.0), "'create' must increment by 1")
	})

	It("increments 'skipped' for a filter-reject", func() {
		taskCreationFilter.SkipReturns(true)
		before := readPRPublishedCounter("skipped")
		beforeCreate := readPRPublishedCounter("create")
		err := runExecutor()
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		after := readPRPublishedCounter("skipped")
		Expect(after-before).To(Equal(1.0), "'skipped' must increment by 1")
		// 'create' must NOT have incremented
		Expect(readPRPublishedCounter("create")-beforeCreate).To(Equal(0.0),
			"'create' must NOT increment for filter-reject")
	})

	It("increments 'skipped' for an untrusted author", func() {
		trustDecision.IsTrustedReturns(trust.NewResult(false, "author not in allowlist"), nil)
		before := readPRPublishedCounter("skipped")
		beforeCreate := readPRPublishedCounter("create")
		err := runExecutor()
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		after := readPRPublishedCounter("skipped")
		Expect(after-before).To(Equal(1.0), "'skipped' must increment by 1")
		Expect(readPRPublishedCounter("create")-beforeCreate).To(Equal(0.0),
			"'create' must NOT increment for untrusted")
	})

	It("increments 'trust_error' for a trust-infra error", func() {
		trustDecision.IsTrustedReturns(nil, errors.Errorf(ctx, "trust lookup failed"))
		before := readPRPublishedCounter("trust_error")
		err := runExecutor()
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
		after := readPRPublishedCounter("trust_error")
		Expect(after-before).To(Equal(1.0), "'trust_error' must increment by 1")
	})

	It("increments 'kafka_error' for a downstream Kafka send error", func() {
		createSender.SendCommandReturns(errors.Errorf(ctx, "kafka send failed"))
		before := readPRPublishedCounter("kafka_error")
		err := runExecutor()
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
		after := readPRPublishedCounter("kafka_error")
		Expect(after-before).To(Equal(1.0), "'kafka_error' must increment by 1")
	})
})

var _ = Describe("prPublishedTotal labels", func() {
	// The label set must include all four labels the executor uses
	// (create, skipped, kafka_error, trust_error) — they are pre-registered
	// in init() at metrics.go:38. We do not assert a specific value because
	// the counter is a process-global: earlier specs may have incremented
	// it. The point of this spec is to prove the labels exist and are
	// readable from the prometheus registry.
	It("exposes create, skipped, trust_error, kafka_error labels", func() {
		for _, label := range []string{"create", "skipped", "trust_error", "kafka_error"} {
			counter := pkg.PRPublishedTotalForTest().WithLabelValues(label)
			// Reading must not panic — the label is pre-registered.
			val := testutil.ToFloat64(counter)
			Expect(val >= 0).To(BeTrue(),
				"label %q must be readable and non-negative, got %v", label, val)
		}
	})
})
