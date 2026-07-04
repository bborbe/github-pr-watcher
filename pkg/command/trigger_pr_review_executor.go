// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"strconv"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/lib/prurl"
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
//   - success                                  → nil, nil, nil
//
// SendResultEnabled is false (spec Non-goal: fire-and-forget, no result topic).
// The github_pr_published metric is incremented from this executor
// (not the HTTP handler) with labels: create, skipped, kafka_error, trust_error.
func NewTriggerPRReviewCommandExecutor(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	metrics pkg.Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		TriggerPRReviewCommandOperation,
		false, // SendResultEnabled = false
		func(ctx context.Context, tx libkv.Tx, commandObject cdb.CommandObject) (*base.EventID, base.Event, error) {
			return runTriggerPRReview(
				ctx, tx, commandObject,
				ghClient, createSender, taskCreationFilter, trustDecision,
				stage, maxSlugLen, maxTitleLen, taskSuffix, metrics,
				currentDateTime,
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
	_ libkv.Tx,
	commandObject cdb.CommandObject,
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	metrics pkg.Metrics,
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
	return publishCreateCommand(
		ctx, prInfo, cmd, details, trustResult, createSender,
		stage, maxSlugLen, maxTitleLen, taskSuffix, metrics,
		currentDateTime,
	)
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
		"trigger executor: filtered pr=%s/%s#%d",
		prInfo.Owner, prInfo.Repo, prInfo.Number,
	)
	return true, errors.Wrapf(
		ctx,
		cdb.ErrCommandObjectSkipped,
		"filter rejected pr=%s/%s#%d",
		prInfo.Owner, prInfo.Repo, prInfo.Number,
	)
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
	trustResult, err := trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: details.AuthorLogin})
	if err != nil {
		// Transient: trust infrastructure error (e.g. allowlist lookup).
		// Framework emits Failure, Kafka redelivers.
		metrics.IncPRPublished("trust_error")
		glog.Errorf("trigger executor: trust check failed pr=%s err=%v", cmd.URL, err)
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

// publishCreateCommand builds the downstream CreateTaskCommand and
// publishes it via createSender. Returns (nil, wrappedErr) on transient
// Kafka send failure and (nil, nil) on success.
//
// When cmd.Force is true, the published TaskIdentifier is derived from
// DeriveTaskIDForce with a time-derived nonce so the agent controller's
// vault-file dedup does not skip the publish. The nonce is intentionally
// not logged — it leaks no security-sensitive data and adds noise without
// operator benefit.
func publishCreateCommand(
	ctx context.Context,
	prInfo *prurl.PRInfo,
	cmd TriggerPRReviewCommand,
	details pkg.PRDetails,
	trustResult trust.Result,
	createSender task.CreateCommandSender,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	metrics pkg.Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) (*base.EventID, base.Event, error) {
	pr := pkg.PullRequest{
		Number:      prInfo.Number,
		Owner:       prInfo.Owner,
		Repo:        prInfo.Repo,
		Title:       details.Title,
		AuthorLogin: details.AuthorLogin,
		HTMLURL:     cmd.URL,
		IsDraft:     details.IsDraft,
	}
	var taskIDStr string
	if cmd.Force {
		nonce := strconv.FormatInt(
			currentDateTime.Now().UnixMicro(), 10,
		)
		taskIDStr = pkg.DeriveTaskIDForce(
			prInfo.Owner, prInfo.Repo, prInfo.Number, details.HeadSHA, nonce,
		).String()
	} else {
		taskIDStr = pkg.DeriveTaskID(
			prInfo.Owner, prInfo.Repo, prInfo.Number, details.HeadSHA,
		).String()
	}

	createCmd := pkg.BuildCreateCommand(
		pr, details, taskIDStr, stage, maxSlugLen, maxTitleLen, taskSuffix,
		trustResult,
	)
	if err := createSender.SendCommand(ctx, createCmd); err != nil {
		// Transient: downstream Kafka send error. Framework emits Failure,
		// Kafka redelivers. Downstream is idempotent via derived task_id.
		metrics.IncPRPublished("kafka_error")
		glog.Errorf("trigger executor: send create-task failed pr=%s err=%v", cmd.URL, err)
		return nil, nil, errors.Wrapf(
			ctx, err, "send create task command for %s", cmd.URL,
		)
	}
	metrics.IncPRPublished("create")
	glog.V(2).Infof(
		"trigger executor: published task_id=%s pr=%s/%s#%d sha=%s force=%v",
		taskIDStr, prInfo.Owner, prInfo.Repo, prInfo.Number, details.HeadSHA, cmd.Force,
	)
	return nil, nil, nil
}
