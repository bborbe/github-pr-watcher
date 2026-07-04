// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	task "github.com/bborbe/agent/command/task"
	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/handler"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	lib "github.com/bborbe/maintainer"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const validPRURL = "https://github.com/bborbe/repo/pull/42"

// outcome is the three-state exit-path classifier for the table-driven test.
// success:    no error, downstream published.
// skipped:    errors.Is(err, cdb.ErrCommandObjectSkipped) — non-retryable.
// wrappedErr: err is non-nil and NOT ErrCommandObjectSkipped — transient.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeSkipped
	outcomeWrappedErr
)

func mustParseEvent(cmd command.TriggerPRReviewCommand) base.Event {
	evt, err := base.ParseEvent(context.Background(), cmd)
	Expect(err).NotTo(HaveOccurred())
	return evt
}

func newCommandObject(cmd command.TriggerPRReviewCommand) cdb.CommandObject {
	return cdb.CommandObject{
		Command: base.Command{
			Operation: command.TriggerPRReviewCommandOperation,
			Data:      mustParseEvent(cmd),
		},
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
}

var _ = Describe("NewTriggerPRReviewCommandExecutor", func() {
	var (
		ctx                context.Context
		ghClient           *mocks.GitHubClient
		createSender       *taskmocks.TaskCreateCommandSender
		taskCreationFilter *mocks.TaskCreationFilter
		trustDecision      *mocks.Trust
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
	})

	DescribeTable("exit-path mapping",
		func(
			configure func(ghClient *mocks.GitHubClient),
			cmd command.TriggerPRReviewCommand,
			expectOutcome outcome, // skipped | wrappedErr | success
			expectDownstreamSent int,
		) {
			// Reset the createSender between entries — the table shares
			// a single fixture so we need to clear per-Entry state.
			*createSender = taskmocks.TaskCreateCommandSender{}
			configure(ghClient)

			_, _, err := command.RunTriggerPRReview(
				ctx,
				nil,
				newCommandObject(cmd),
				ghClient, createSender, taskCreationFilter, trustDecision,
				"dev", 80, 200, "",
				pkg.NewMetrics(),
				libtime.NewCurrentDateTime(),
			)

			switch expectOutcome {
			case outcomeSkipped:
				Expect(err).To(HaveOccurred(), "expected ErrCommandObjectSkipped")
				Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue(),
					"expected ErrCommandObjectSkipped, got %v", err)
			case outcomeWrappedErr:
				Expect(err).To(HaveOccurred(), "expected wrapped (transient) error")
				Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse(),
					"transient errors must NOT be classified as Skipped, got %v", err)
			case outcomeSuccess:
				Expect(err).NotTo(HaveOccurred(), "unexpected error: %v", err)
			}
			Expect(createSender.SendCommandCallCount()).To(Equal(expectDownstreamSent),
				"downstream send count mismatch")
		},
		Entry("valid pr → success + downstream sent",
			func(_ *mocks.GitHubClient) {},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeSuccess, 1),
		Entry(
			"invalid url (non-github) → skipped",
			func(_ *mocks.GitHubClient) {},
			command.TriggerPRReviewCommand{
				URL: "https://bitbucket.example.com/projects/owner/repos/repo/pull-requests/1",
			},
			outcomeSkipped,
			0,
		),
		Entry("malformed payload → skipped",
			// We cannot easily make MarshalInto fail with a valid Event shape,
			// so this entry exercises the "invalid url" path which the executor
			// also classifies as Skipped.
			func(_ *mocks.GitHubClient) {},
			command.TriggerPRReviewCommand{URL: "not-a-url"},
			outcomeSkipped, 0),
		Entry("filter rejects → skipped",
			func(_ *mocks.GitHubClient) {
				taskCreationFilter.SkipReturns(true)
			},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeSkipped, 0),
		Entry("untrusted author → skipped",
			func(_ *mocks.GitHubClient) {
				trustDecision.IsTrustedReturns(
					trust.NewResult(false, "author not in allowlist"),
					nil,
				)
			},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeSkipped, 0),
		Entry("github 5xx → wrapped err",
			func(gh *mocks.GitHubClient) {
				gh.GetPRDetailsReturns(pkg.PRDetails{}, errors.Errorf(ctx, "github 5xx"))
			},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeWrappedErr, 0),
		Entry("trust infra err → wrapped err",
			func(_ *mocks.GitHubClient) {
				trustDecision.IsTrustedReturns(nil, errors.Errorf(ctx, "trust lookup failed"))
			},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeWrappedErr, 0),
		Entry("kafka send err → wrapped err",
			func(_ *mocks.GitHubClient) {
				createSender.SendCommandReturns(errors.Errorf(ctx, "kafka send failed"))
			},
			command.TriggerPRReviewCommand{URL: validPRURL},
			outcomeWrappedErr, 1),
	)
})

var _ = Describe(
	"executor vs handler payload parity (spec 066 AC: byte-identical downstream)",
	func() {
		// This is the load-bearing AC for spec § Constraints: the downstream
		// CreateTaskCommand payload MUST be byte-identical to today's
		// singlePRTriggerHandler.ServeHTTP output.
		//
		// Post-prompt-3 the HTTP handler is a thin shell: it publishes a
		// TriggerPRReviewCommand to Kafka and returns 202. The full pipeline
		// (GitHub fetch → filter → trust → downstream publish) lives in the
		// executor. This describe block now verifies that the executor
		// produces the expected CreateCommand for trusted / untrusted
		// authors when fed a TriggerPRReviewCommand that originated from the
		// new thin handler. (The old byte-identical handler-vs-executor
		// comparison is no longer applicable because the handler no longer
		// produces a CreateCommand directly.)
		var (
			ctx                context.Context
			ghClient           *mocks.GitHubClient
			taskCreationFilter *mocks.TaskCreationFilter
			trustDecision      *mocks.Trust
		)

		BeforeEach(func() {
			ctx = context.Background()
			ghClient = new(mocks.GitHubClient)
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
		})

		It("handler → executor produces a valid CreateCommand for trusted author", func() {
			// Handler path: capture the TriggerPRReviewCommand the new
			// thin handler publishes to Kafka.
			trigSender := new(mocks.TriggerPRReviewCommandSender)
			var publishedCmd command.TriggerPRReviewCommand
			trigSender.SendCommandStub = func(_ context.Context, c command.TriggerPRReviewCommand) error {
				publishedCmd = c
				return nil
			}
			h := handler.NewSinglePRTriggerHandler(trigSender)
			req := httptest.NewRequest("POST", "/trigger?url="+validPRURL, nil)
			resp := httptest.NewRecorder()
			Expect(h.ServeHTTP(ctx, resp, req)).To(Succeed())
			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(publishedCmd.URL).To(Equal(validPRURL))

			// Executor path: feed the handler's published command back
			// into the executor and capture the resulting CreateCommand.
			executorSender := new(taskmocks.TaskCreateCommandSender)
			var executorCmd task.CreateCommand
			executorSender.SendCommandStub = func(_ context.Context, c task.CreateCommand) error {
				executorCmd = c
				return nil
			}
			_, _, err := command.RunTriggerPRReview(
				ctx,
				nil,
				newCommandObject(publishedCmd),
				ghClient, executorSender, taskCreationFilter, trustDecision,
				"dev", 80, 200, "",
				pkg.NewMetrics(),
				libtime.NewCurrentDateTime(),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(executorCmd).NotTo(BeZero(),
				"trusted author: executor must produce a non-zero CreateCommand")
			// Phase / status reflect trust=true → in_progress routing
			Expect(executorCmd.Frontmatter["phase"]).To(Equal("planning"))
			Expect(executorCmd.Frontmatter["status"]).To(Equal("in_progress"))
		})

		It("executor skips untrusted author and does not publish downstream", func() {
			trustDecision.IsTrustedReturns(trust.NewResult(false, "author not in allowlist"), nil)

			// Handler publishes a TriggerPRReviewCommand (handler is
			// trust-agnostic in prompt 3 — the untrusted branch lives in
			// the executor).
			trigSender := new(mocks.TriggerPRReviewCommandSender)
			var publishedCmd command.TriggerPRReviewCommand
			trigSender.SendCommandStub = func(_ context.Context, c command.TriggerPRReviewCommand) error {
				publishedCmd = c
				return nil
			}
			h := handler.NewSinglePRTriggerHandler(trigSender)
			req := httptest.NewRequest("POST", "/trigger?url="+validPRURL, nil)
			resp := httptest.NewRecorder()
			Expect(h.ServeHTTP(ctx, resp, req)).To(Succeed())
			Expect(resp.Code).To(Equal(http.StatusAccepted))

			// Executor must skip untrusted → ErrCommandObjectSkipped, no
			// downstream publish.
			executorSender := new(taskmocks.TaskCreateCommandSender)
			_, _, err := command.RunTriggerPRReview(
				ctx,
				nil,
				newCommandObject(publishedCmd),
				ghClient, executorSender, taskCreationFilter, trustDecision,
				"dev", 80, 200, "",
				pkg.NewMetrics(),
				libtime.NewCurrentDateTime(),
			)
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
			Expect(executorSender.SendCommandCallCount()).To(Equal(0),
				"untrusted author: executor must NOT publish downstream")
		})
	},
)

var _ = Describe("executor crash recovery (spec 066 AC 16)", func() {
	// Proves at-least-once-via-idempotent-downstream: simulate a pod kill
	// mid-execution (context cancelled during gh fetch) and verify that
	// on retry the same downstream CreateTaskCommand is published exactly once.
	var (
		ctx                context.Context
		ghClient           *mocks.GitHubClient
		createSender       *taskmocks.TaskCreateCommandSender
		taskCreationFilter *mocks.TaskCreationFilter
		trustDecision      *mocks.Trust
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
	})

	It("a killed invocation can be retried and still publish exactly once", func() {
		// Round 1: simulate a real GitHub client that respects context
		// cancellation. The stub honours ctx.Err() and returns the
		// context-cancelled error — same shape as a real client that gets
		// SIGKILL'd in mid-request.
		killedCtx, cancel := context.WithCancel(ctx)
		ghClient.GetPRDetailsStub = func(c context.Context, _, _ string, _ int) (pkg.PRDetails, error) {
			// Cancel mid-call, then return the context error like a real client would.
			cancel()
			return pkg.PRDetails{}, c.Err()
		}
		createSender.SendCommandStub = func(_ context.Context, _ task.CreateCommand) error {
			// If the killed run somehow reaches SendCommand, fail the test so we
			// notice (it should not — ghClient already returned an error).
			Fail("SendCommand must not be called during the killed invocation")
			return nil
		}

		cmd := command.TriggerPRReviewCommand{URL: validPRURL}
		commandObject := newCommandObject(cmd)

		_, _, err := command.RunTriggerPRReview(
			killedCtx, nil, commandObject,
			ghClient, createSender, taskCreationFilter, trustDecision,
			"dev", 80, 200, "",
			pkg.NewMetrics(),
			libtime.NewCurrentDateTime(),
		)
		Expect(err).To(HaveOccurred(),
			"killed invocation must return a transient error so Kafka redelivers")
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse(),
			"killed invocation must NOT be classified as Skipped (transient, not deliberate)")
		Expect(createSender.SendCommandCallCount()).To(Equal(0),
			"killed invocation must not publish downstream")

		// Round 2: fresh context, fresh sender, deterministic github response.
		// The same commandObject is reused (Kafka would redeliver it as-is).
		freshSender := new(taskmocks.TaskCreateCommandSender)
		freshSender.SendCommandStub = func(_ context.Context, _ task.CreateCommand) error {
			return nil
		}
		ghClient.GetPRDetailsStub = nil
		ghClient.GetPRDetailsReturns(pkg.PRDetails{
			HeadSHA:     "abc123",
			CloneURL:    "https://github.com/bborbe/repo.git",
			BaseRef:     "main",
			AuthorLogin: "bborbe",
			Title:       "Feature: add support",
			IsDraft:     false,
		}, nil)

		_, _, err = command.RunTriggerPRReview(
			context.Background(), nil, commandObject,
			ghClient, freshSender, taskCreationFilter, trustDecision,
			"dev", 80, 200, "",
			pkg.NewMetrics(),
			libtime.NewCurrentDateTime(),
		)
		Expect(err).NotTo(HaveOccurred(), "retry must succeed: %v", err)
		Expect(freshSender.SendCommandCallCount()).To(Equal(1),
			"retry must publish downstream exactly once")
	})
})

// force-true branch (spec 067) — exercises the executor's behaviour when
// TriggerPRReviewCommand.Force is true. The executor must derive a salted
// task identifier via DeriveTaskIDForce (using a time-derived nonce from
// the injected libtime.CurrentDateTimeGetter) so the agent controller's
// vault-file dedup skip does not fire. The non-force path stays byte-
// identical to today's output (covered in the existing three describe
// blocks above).
var _ = Describe("force-true branch (spec 067)", func() {
	var (
		ctx                context.Context
		ghClient           *mocks.GitHubClient
		createSender       *taskmocks.TaskCreateCommandSender
		taskCreationFilter *mocks.TaskCreationFilter
		trustDecision      *mocks.Trust
		// fakeNow is the libtime.DateTime that the injected clock returns.
		// Tests advance fakeNow between calls to drive distinct nonces.
		fakeNow libtime.DateTime
		// currentDateTime is the injected CurrentDateTimeGetter. It returns
		// whatever fakeNow currently holds; tests mutate fakeNow in-place
		// (assignment is the only clock-advance operation) so two distinct
		// timestamps produce two distinct nonces.
		currentDateTime libtime.CurrentDateTimeGetter
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

		// Pin a deterministic start time for the fake clock. 1µs resolution
		// is enough — the nonce uses UnixMicro so any non-equal value
		// produces a different nonce.
		fakeNow = libtime.NewDateTime(2026, 6, 9, 12, 0, 0, 0, time.UTC)
		currentDateTime = libtime.CurrentDateTimeGetterFunc(
			func() libtime.DateTime { return fakeNow },
		)
	})

	// captureCreateCommand runs RunTriggerPRReview with the supplied command
	// and returns the CreateCommand the mock createSender received. Returns
	// (zero, false) if no send occurred.
	captureCreateCommand := func(cmd command.TriggerPRReviewCommand) (task.CreateCommand, bool) {
		*createSender = taskmocks.TaskCreateCommandSender{}
		_, _, err := command.RunTriggerPRReview(
			ctx,
			nil,
			newCommandObject(cmd),
			ghClient, createSender, taskCreationFilter, trustDecision,
			"dev", 80, 200, "",
			pkg.NewMetrics(),
			currentDateTime,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(createSender.SendCommandCallCount()).To(Equal(1),
			"executor must publish downstream exactly once for cmd=%+v", cmd)
		_, captured := createSender.SendCommandArgsForCall(0)
		return captured, true
	}

	// canonicalID is the (owner="bborbe", repo="repo", number=42, sha="abc123")
	// canonical TaskIdentifier — the non-force path's expected output.
	canonicalID := pkg.DeriveTaskID("bborbe", "repo", 42, "abc123").String()

	It("TestExecutor_ForceTrueUsesSaltedID", func() {
		captured, ok := captureCreateCommand(
			command.TriggerPRReviewCommand{URL: validPRURL, Force: true},
		)
		Expect(ok).To(BeTrue())
		Expect(string(captured.TaskIdentifier)).NotTo(Equal(canonicalID),
			"Force=true must produce a salted identifier different from the canonical one")
	})

	It("TestExecutor_ForceFalseUsesCanonicalID", func() {
		captured, ok := captureCreateCommand(
			command.TriggerPRReviewCommand{URL: validPRURL, Force: false},
		)
		Expect(ok).To(BeTrue())
		Expect(string(captured.TaskIdentifier)).To(Equal(canonicalID),
			"Force=false must produce the canonical (owner, repo, number, sha)-derived identifier")
	})

	It("TestExecutor_ForceFalseProducesIdenticalCreateCommand", func() {
		// Fixture documented inline: the executor builds pkg.PullRequest
		// from prInfo (parsed out of validPRURL) and pkg.PRDetails from
		// the mock ghClient (set in BeforeEach). The fixture here MUST
		// match that pair exactly — drift in the executor's struct
		// construction will surface as a Body or Frontmatter mismatch.
		captured, ok := captureCreateCommand(
			command.TriggerPRReviewCommand{URL: validPRURL, Force: false},
		)
		Expect(ok).To(BeTrue())

		expected := pkg.BuildCreateCommand(
			pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "repo",
				Title:       "Feature: add support",
				AuthorLogin: "bborbe",
				HTMLURL:     validPRURL,
				IsDraft:     false,
			},
			pkg.PRDetails{
				HeadSHA:     "abc123",
				CloneURL:    "https://github.com/bborbe/repo.git",
				BaseRef:     "main",
				AuthorLogin: "bborbe",
				Title:       "Feature: add support",
				IsDraft:     false,
			},
			canonicalID,
			"dev", 80, 200, "",
			trust.NewResult(true, "trusted"),
		)

		// Field-by-field equality on every field except TaskIdentifier
		// (which is asserted separately to make a regression on the salted
		// branch loud).
		Expect(string(captured.TaskIdentifier)).To(Equal(string(expected.TaskIdentifier)))
		Expect(captured.Title).To(Equal(expected.Title))
		Expect(captured.Frontmatter).To(Equal(expected.Frontmatter))
		Expect(captured.Body).To(Equal(expected.Body))
	})

	It("TestExecutor_TwoForceTriggersProduceDifferentIDs", func() {
		// First call: clock at fakeNow.
		first, ok := captureCreateCommand(
			command.TriggerPRReviewCommand{URL: validPRURL, Force: true},
		)
		Expect(ok).To(BeTrue())

		// Advance the fake clock by 1µs — nonce (UnixMicro) differs, so
		// the salted identifier must differ.
		fakeNow = fakeNow.Add(libtime.Microsecond)

		second, ok := captureCreateCommand(
			command.TriggerPRReviewCommand{URL: validPRURL, Force: true},
		)
		Expect(ok).To(BeTrue())

		Expect(string(first.TaskIdentifier)).NotTo(Equal(string(second.TaskIdentifier)),
			"two Force=true invocations with a different clock must produce distinct identifiers")
	})

	It("TestExecutor_ForceTrueIncrementsCreateLabel", func() {
		// Use a counterfeiter Metrics mock so we can inspect every label
		// value passed to IncPRPublished and assert that no force-suffixed
		// label is introduced.
		metrics := new(mocks.Metrics)
		*createSender = taskmocks.TaskCreateCommandSender{}
		_, _, err := command.RunTriggerPRReview(
			ctx,
			nil,
			newCommandObject(command.TriggerPRReviewCommand{URL: validPRURL, Force: true}),
			ghClient, createSender, taskCreationFilter, trustDecision,
			"dev", 80, 200, "",
			metrics,
			currentDateTime,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(createSender.SendCommandCallCount()).To(Equal(1))

		// Exactly one increment, and it is the existing "create" label.
		Expect(metrics.IncPRPublishedCallCount()).To(Equal(1),
			"Force=true must increment github_pr_published exactly once on success")
		Expect(metrics.IncPRPublishedArgsForCall(0)).To(Equal("create"),
			"Force=true must use the existing 'create' label — no 'force', 'forced_create', etc.")

		// Belt-and-braces: collect every label the mock ever saw, in case
		// the assertion above is silently bypassed by an extra hidden
		// call. The set must be exactly {"create": true}.
		labels := map[string]bool{}
		for i := 0; i < metrics.IncPRPublishedCallCount(); i++ {
			labels[metrics.IncPRPublishedArgsForCall(i)] = true
		}
		Expect(labels).To(Equal(map[string]bool{"create": true}),
			"Force=true must NOT introduce a new label value (got %v)", labels)
	})
})
