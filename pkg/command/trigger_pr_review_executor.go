// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"slices"
	"strconv"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/prurl"
)

// NewTriggerPRReviewCommandExecutor creates a cdb.CommandObjectExecutorTx that
// consumes TriggerPRReviewCommand messages and drives the single-PR review
// pipeline: GitHub fetch → filter → trust → downstream publish.
//
// Exit-path mapping (per spec 066 § Desired Behavior 5):
//   - invalid URL (Validate fails)            → cdb.ErrCommandObjectSkipped
//   - filter-rejected PR (filter.Skip==true)  → cdb.ErrCommandObjectSkipped
//   - untrusted author (trust.IsTrusted==false) → cdb.ErrCommandObjectSkipped
//   - GitHub 5xx / network error              → wrapped error (transient, retried)
//   - trust infrastructure error              → wrapped error (transient, retried)
//   - downstream CreateTaskCommand send error → wrapped error (transient, retried)
//   - in-flight duplicate (forced)             → cdb.ErrCommandObjectSkipped from the guard
//   - success                                  → nil, nil, nil
//
// SendResultEnabled is false (spec Non-goal: fire-and-forget, no result topic).
// The github_pr_published metric is incremented from this executor
// (not the HTTP handler) with labels: create, skipped, kafka_error, trust_error,
// deferred, inflight_skipped.
func NewTriggerPRReviewCommandExecutor(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	mechanicalOverrideAuthors []string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
	maxAdditions int,
	maxChangedFiles int,
	metrics pkg.Metrics,
	retryGuard pkg.RetryGuard,
	overrideLedger pkg.OverrideLedger,
	coalescer coalesce.Coalescer,
	currentDateTime libtime.CurrentDateTimeGetter,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		TriggerPRReviewCommandOperation,
		false, // SendResultEnabled = false
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			return runTriggerPRReview(
				ctx, tx, commandObject,
				ghClient, createSender, taskCreationFilter, trustDecision,
				mechanicalOverrideAuthors,
				stage, maxSlugLen, maxTitleLen, taskSuffix, targetVault,
				maxAdditions, maxChangedFiles, metrics,
				retryGuard, overrideLedger, coalescer, currentDateTime,
			)
		},
	)
}

// runTriggerPRReview is the work-loop for a single TriggerPRReviewCommand.
// Splitting it out from the constructor (a) keeps the constructor's
// closure short and (b) makes the function directly testable from
// the package's external _test.go (the constructor returns an interface,
// not a closure).
//
// cmd.Validate is invoked here as a defense-in-depth: the sender already
// validates before publishing, but a buggy client that bypasses the
// HTTP handler could otherwise inject garbage. The framework's
// CommandObject.Validate only checks the wrapper (SchemaID + base.Command),
// not the typed payload.
func runTriggerPRReview(
	ctx context.Context,
	tx libkv.Tx,
	commandObject cdb.CommandObject,
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	mechanicalOverrideAuthors []string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
	maxAdditions int,
	maxChangedFiles int,
	metrics pkg.Metrics,
	retryGuard pkg.RetryGuard,
	overrideLedger pkg.OverrideLedger,
	coalescer coalesce.Coalescer,
	currentDateTime libtime.CurrentDateTimeGetter,
) (*base.EventID, base.Event, error) {
	cmd, err := unmarshalAndValidate(ctx, commandObject)
	if err != nil {
		return nil, nil, err
	}
	prInfo, err := prurl.ParsePRURL(ctx, cmd.URL)
	if err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"parse url %q: %v",
			cmd.URL,
			err,
		)
	}
	details, err := ghClient.GetPRDetails(ctx, prInfo.Owner, prInfo.Repo, prInfo.Number)
	if err != nil {
		// Transient: GitHub 5xx / network error. Framework emits Failure
		// on the result topic, Kafka redelivers.
		return nil, nil, errors.Wrapf(ctx, err, "get PR details for %s", cmd.URL)
	}
	if skip, err := applyFilter(
		ctx, prInfo, details, taskCreationFilter, metrics,
	); err != nil || skip {
		return nil, nil, err
	}
	trustResult, err := applyTrust(ctx, cmd, details, trustDecision, metrics)
	if err != nil || trustResult == nil {
		return nil, nil, err
	}
	// Defer a WEBHOOK-created command for a mechanical-override author to the
	// poll path, which owns the mechanical decision. A webhook arrives seconds
	// after the PR is created — always before the poll path can act — so
	// publishing a full review here guarantees that a trivial bot bump gets
	// BOTH an LLM review and a mechanical override on the same SHA, spending
	// exactly the reviewer pod the override exists to save. The poll path
	// publishes either the override or, when the PR does not classify
	// trivial, the review; nothing is lost, because applyFilter above already
	// rejected every PR the poll path would reject too.
	//
	// The gate is cmd.Webhook, NOT cmd.Force: the plain /trigger endpoint also
	// publishes Force=false, so keying on Force would silently swallow an
	// operator's explicit review request. An operator command — forced or not
	// — always gets a full review.
	if err := deferWebhookMechanicalOverride(
		ctx, cmd, prInfo, details, mechanicalOverrideAuthors, metrics, coalescer,
	); err != nil {
		return nil, nil, err
	}
	// The in-flight guard runs after every existing skip path, so an invalid
	// URL, a filter skip, an untrusted author or a webhook deferral never
	// touches the ledger. It is consulted only for a forced trigger.
	guardPR := newRetryGuardPR(prInfo, details)
	// A review is never published for a SHA a fresh override owns, whoever asked.
	// The deferral above covers only webhook commands; the ledger covers every
	// producer — bborbe/kafka-topic-reader#26 published a review 44 s after its
	// override because its command arrived with Webhook=false.
	if err := suppressForOverride(ctx, tx, overrideLedger, guardPR, metrics); err != nil {
		return nil, nil, err
	}
	if err := checkRetryGuard(ctx, tx, retryGuard, cmd, guardPR); err != nil {
		return nil, nil, err
	}
	// Derive the identifier only after the guard let this trigger through: a
	// suppressed duplicate publishes nothing and so derives nothing.
	taskIDStr := deriveTaskID(prInfo, cmd, details.HeadSHA, currentDateTime)
	// A forced re-review bypasses the park regardless, but the exclusion is
	// still computed here: it feeds the `.reviewignore` audit line on the task
	// body, which must appear on every review task, not only parked ones.
	exclusion := pkg.ReviewIgnoreExclusion(
		ctx, ghClient, prInfo.Owner, prInfo.Repo, prInfo.Number, cmd.URL,
	)
	if err := publishCreateCommand(
		ctx, prInfo, cmd, details, trustResult, createSender, taskIDStr,
		stage, maxSlugLen, maxTitleLen, taskSuffix, targetVault,
		maxAdditions, maxChangedFiles, metrics, exclusion,
	); err != nil {
		return nil, nil, err
	}
	recordRetryGuard(ctx, tx, retryGuard, guardPR, taskIDStr)
	return nil, nil, nil
}

// newRetryGuardPR builds the guard key for one publishable review target.
func newRetryGuardPR(
	prInfo *prurl.PRInfo,
	details pkg.PRDetails,
) pkg.RetryGuardPR {
	return pkg.RetryGuardPR{
		Owner:   prInfo.Owner,
		Repo:    prInfo.Repo,
		Number:  prInfo.Number,
		HeadSHA: details.HeadSHA,
	}
}

// deferWebhookMechanicalOverride defers a WEBHOOK-created command from a
// mechanical-override author to the poll path, which owns the mechanical
// decision. It returns the skip error to propagate, or nil to continue.
//
// The gate is cmd.Webhook, NOT cmd.Force: the plain /trigger endpoint also
// publishes Force=false, so keying on Force would silently swallow an
// operator's explicit review request. An operator command — forced or not —
// always gets a full review from this deferral. The ledger gate below is a
// separate decision and deliberately does override that promise: an override
// already owns the SHA, whoever asked.
func deferWebhookMechanicalOverride(
	ctx context.Context,
	cmd TriggerPRReviewCommand,
	prInfo *prurl.PRInfo,
	details pkg.PRDetails,
	mechanicalOverrideAuthors []string,
	metrics pkg.Metrics,
	coalescer coalesce.Coalescer,
) error {
	if !cmd.Webhook || cmd.Force ||
		!slices.Contains(mechanicalOverrideAuthors, details.AuthorLogin) {
		return nil
	}
	// The webhook handler claimed this head when its command reached Kafka, on
	// MarkEmitted's contract that a record was published for it. This deferral
	// is the opposite outcome: nothing was published, and the poll path owns
	// the decision. Leaving the claim in place would make that poll path read
	// Settled from Admit and publish nothing either, so a PR whose override
	// then does not qualify is stranded until the unreviewed scanner's
	// threshold fires and spends the full review this deferral exists to save.
	coalescer.Release(
		coalesce.Key{Owner: prInfo.Owner, Repo: prInfo.Repo, Number: prInfo.Number},
		details.HeadSHA,
	)
	metrics.IncPRPublished("deferred")
	glog.V(2).Infof(
		"trigger executor: deferred to poll path for mechanical override pr=%s/%s#%d author=%s",
		prInfo.Owner, prInfo.Repo, prInfo.Number, details.AuthorLogin,
	)
	return errors.Wrapf(
		ctx,
		cdb.ErrCommandObjectSkipped,
		"mechanical-override author deferred to poll path pr=%s/%s#%d",
		prInfo.Owner, prInfo.Repo, prInfo.Number,
	)
}

// suppressForOverride refuses to publish a review for a head SHA that a fresh
// pr-override already owns. It returns the guard's cdb.ErrCommandObjectSkipped
// so the framework treats the refusal as a skip rather than a failure — the
// same contract the webhook deferral above uses.
//
// A ledger that cannot be read fails OPEN and the review is published: the
// pre-change behaviour is to publish, and losing a review is worse than the
// duplicate this suppresses.
func suppressForOverride(
	ctx context.Context,
	tx libkv.Tx,
	overrideLedger pkg.OverrideLedger,
	pr pkg.RetryGuardPR,
	metrics pkg.Metrics,
) error {
	if overrideLedger == nil {
		return nil
	}
	suppressed, err := overrideLedger.SuppressedTx(ctx, tx, pr)
	if err != nil {
		glog.Warningf(
			"trigger executor: override ledger read failed, publishing review pr=%s/%s#%d sha=%s err=%v",
			pr.Owner,
			pr.Repo,
			pr.Number,
			pr.HeadSHA,
			err,
		)
		return nil
	}
	if !suppressed {
		return nil
	}
	metrics.IncPRPublished("override_owns_sha")
	glog.V(2).Infof(
		"trigger executor: review suppressed, override owns sha pr=%s/%s#%d sha=%s",
		pr.Owner, pr.Repo, pr.Number, pr.HeadSHA,
	)
	return errors.Wrapf(
		ctx, cdb.ErrCommandObjectSkipped,
		"override owns sha pr=%s/%s#%d sha=%s",
		pr.Owner, pr.Repo, pr.Number, pr.HeadSHA,
	)
}

// checkRetryGuard consults the in-flight guard for a forced trigger. An
// unforced trigger never reaches the guard. The returned error includes the
// guard's cdb.ErrCommandObjectSkipped for an in-flight duplicate and any
// transient ledger error; it is returned unchanged so errors.Is still matches.
func checkRetryGuard(
	ctx context.Context,
	tx libkv.Tx,
	retryGuard pkg.RetryGuard,
	cmd TriggerPRReviewCommand,
	guardPR pkg.RetryGuardPR,
) error {
	if !cmd.Force {
		return nil
	}
	_, err := retryGuard.Check(ctx, tx, guardPR)
	return err
}

// recordRetryGuard notes a successful publish in the guard's ledger, whether
// the trigger was forced or not. The ledger must cover every publish that
// creates a review task: a forced retry normally follows a poll-path or
// webhook publish, and a ledger holding only forced publishes would let that
// retry duplicate the review already running. A record failure is logged, never
// returned: the create command was already sent, and surfacing an error would
// make the framework redeliver it.
func recordRetryGuard(
	ctx context.Context,
	tx libkv.Tx,
	retryGuard pkg.RetryGuard,
	guardPR pkg.RetryGuardPR,
	taskIDStr string,
) {
	if err := retryGuard.Record(ctx, tx, guardPR, taskIDStr); err != nil {
		glog.Warningf(
			"retry guard: record publish failed pr=%s/%s#%d sha=%s task_id=%s err=%v",
			guardPR.Owner, guardPR.Repo, guardPR.Number, guardPR.HeadSHA, taskIDStr, err,
		)
	}
}

// unmarshalAndValidate decodes the CommandObject payload into a typed
// TriggerPRReviewCommand and runs Validate as defense-in-depth. Any
// failure here is a deliberate, non-retryable skip.
func unmarshalAndValidate(
	ctx context.Context,
	commandObject cdb.CommandObject,
) (TriggerPRReviewCommand, error) {
	var cmd TriggerPRReviewCommand
	if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
		return cmd, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"malformed TriggerPRReviewCommand: %v",
			err,
		)
	}
	if err := cmd.Validate(ctx); err != nil {
		return cmd, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"validate TriggerPRReviewCommand: %v",
			err,
		)
	}
	return cmd, nil
}

// applyFilter evaluates the TaskCreationFilter chain against the PR.
// Returns (true, nil) when the PR is filter-skipped (non-retryable).
// Returns (false, wrappedErr) on transient failures.
// Returns (false, nil) when the PR passes the filter.
func applyFilter(
	ctx context.Context,
	prInfo *prurl.PRInfo,
	details pkg.PRDetails,
	taskCreationFilter filter.TaskCreationFilter,
	metrics pkg.Metrics,
) (bool, error) {
	filterPR := filter.PR{
		AuthorLogin: details.AuthorLogin,
		IsDraft:     details.IsDraft,
		Title:       details.Title,
		UpdatedAt:   details.UpdatedAt,
		RepoKey:     "github.com/" + prInfo.Owner + "/" + prInfo.Repo,
	}
	if !taskCreationFilter.Skip(filterPR) {
		return false, nil
	}
	metrics.IncPRPublished("skipped")
	glog.V(2).Infof(
		"trigger executor: filtered pr=%s/%s#%d reason=%s",
		prInfo.Owner, prInfo.Repo, prInfo.Number,
		filterSkipReason(taskCreationFilter, filterPR),
	)
	return true, errors.Wrapf(
		ctx,
		cdb.ErrCommandObjectSkipped,
		"filter rejected pr=%s/%s#%d",
		prInfo.Owner, prInfo.Repo, prInfo.Number,
	)
}

// filterSkipReason resolves the identity of the filter that voted skip, so
// the "filtered" log line names WHICH member of the chain fired rather than
// logging one undifferentiated line for every skip cause.
//
// The chain is normally passed as a filter.TaskCreationFilters composite,
// whose SkippingFilter names the first (and only reachable) voter. A bare
// (non-chain) filter has no members, so we fall back to its own Name(). A
// nil chain is guarded so the log path can never panic.
func filterSkipReason(
	taskCreationFilter filter.TaskCreationFilter,
	pr filter.PR,
) string {
	if taskCreationFilter == nil {
		return "unknown"
	}
	if chain, ok := taskCreationFilter.(filter.TaskCreationFilters); ok {
		if skipping := chain.SkippingFilter(pr); skipping != nil {
			return skipping.Name()
		}
	}
	return taskCreationFilter.Name()
}

// applyTrust evaluates the trust decision for the PR author. Returns
// (nil, wrappedErr) on transient trust infrastructure failure, (nil, skippedErr)
// on a deliberate rejection (non-retryable), and (result, nil) on a
// successful trust check.
func applyTrust(
	ctx context.Context,
	cmd TriggerPRReviewCommand,
	details pkg.PRDetails,
	trustDecision trust.Trust,
	metrics pkg.Metrics,
) (trust.Result, error) {
	trustResult, err := trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      details.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		// Transient: trust infrastructure error (e.g. allowlist lookup).
		// Framework emits Failure and logs at the boundary, Kafka redelivers —
		// so wrap and return rather than logging here too (no-log-and-return-error).
		metrics.IncPRPublished("trust_error")
		return nil, errors.Wrapf(ctx, err, "check trust for %s", cmd.URL)
	}
	if trustResult.Success() {
		return trustResult, nil
	}
	// Deliberate: author not on allowlist. Non-retryable.
	metrics.IncPRPublished("skipped")
	glog.V(2).Infof(
		"trigger executor: untrusted author pr=%s author=%s reason=%s",
		cmd.URL, details.AuthorLogin, trustResult.Description(),
	)
	return nil, errors.Wrapf(
		ctx,
		cdb.ErrCommandObjectSkipped,
		"untrusted author %s for pr %s",
		details.AuthorLogin, cmd.URL,
	)
}

// deriveTaskID returns the TaskIdentifier to publish for this PR at headSHA.
//
// When cmd.Force is true, the identifier is derived from DeriveTaskIDForce
// with a time-derived nonce, AND cmd.Force is passed to BuildCreateCommand so
// the vault TITLE carries a retry marker too. Both are required: the agent
// controller dedupes on title path (checkTitlePathFree), not on
// task_identifier, so salting the identifier alone left every forced
// re-review of an unchanged head SHA silently rejected with
// ErrTaskAlreadyExists (confirmed in prod, bborbe/coding#90, 2026-08-09).
// The nonce is intentionally not logged — it leaks no security-sensitive data
// and adds noise without operator benefit.
func deriveTaskID(
	prInfo *prurl.PRInfo,
	cmd TriggerPRReviewCommand,
	headSHA string,
	currentDateTime libtime.CurrentDateTimeGetter,
) string {
	if cmd.Force {
		nonce := strconv.FormatInt(currentDateTime.Now().UnixMicro(), 10)
		return pkg.DeriveTaskIDForce(
			prInfo.Owner, prInfo.Repo, prInfo.Number, headSHA, nonce,
		).String()
	}
	return pkg.DeriveTaskID(
		prInfo.Owner, prInfo.Repo, prInfo.Number, headSHA,
	).String()
}

// publishCreateCommand builds the downstream CreateTaskCommand and
// publishes it via createSender. Returns a wrapped error on transient Kafka
// send failure and nil on success.
func publishCreateCommand(
	ctx context.Context,
	prInfo *prurl.PRInfo,
	cmd TriggerPRReviewCommand,
	details pkg.PRDetails,
	trustResult trust.Result,
	createSender task.CreateCommandSender,
	taskIDStr string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
	maxAdditions int,
	maxChangedFiles int,
	metrics pkg.Metrics,
	exclusion pkg.Exclusion,
) error {
	pr := pkg.PullRequest{
		Number:      prInfo.Number,
		Owner:       prInfo.Owner,
		Repo:        prInfo.Repo,
		Title:       details.Title,
		AuthorLogin: details.AuthorLogin,
		HTMLURL:     cmd.URL,
		IsDraft:     details.IsDraft,
	}

	createCmd := pkg.BuildCreateCommand(
		pr, details, taskIDStr, stage, maxSlugLen, maxTitleLen, taskSuffix,
		targetVault, trustResult, cmd.Force, maxAdditions, maxChangedFiles,
		exclusion,
	)
	if err := createSender.SendCommand(ctx, createCmd); err != nil {
		// Transient: downstream Kafka send error. Framework emits Failure and
		// logs at the boundary, Kafka redelivers — so wrap and return rather
		// than logging here too (no-log-and-return-error). Downstream is
		// idempotent via derived task_id.
		metrics.IncPRPublished("kafka_error")
		return errors.Wrapf(
			ctx, err, "send create task command for %s", cmd.URL,
		)
	}
	metrics.IncPRPublished("create")
	glog.V(2).Infof(
		"trigger executor: published task_id=%s pr=%s/%s#%d sha=%s force=%v",
		taskIDStr, prInfo.Owner, prInfo.Repo, prInfo.Number, details.HeadSHA, cmd.Force,
	)
	return nil
}
