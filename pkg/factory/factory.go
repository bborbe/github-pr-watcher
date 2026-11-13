// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package factory wires concrete dependencies for the maintainer-watcher-github-pr binary.
package factory

import (
	"context"
	"net/http"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/escalate"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/bborbe/notification/command/notification"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"

	lib "github.com/bborbe/maintainer"
	"github.com/bborbe/maintainer/githubapp"
)

// CreateGitHubAppClient creates an HTTP client authenticated as a GitHub App installation.
func CreateGitHubAppClient(
	ctx context.Context,
	appID int64,
	installationID int64,
	pemKey []byte,
) (*http.Client, error) {
	cfg := githubapp.Config{
		AppID:          appID,
		InstallationID: installationID,
		PEM:            pemKey,
	}
	return githubapp.NewClient(ctx, cfg)
}

// CreateKafkaSender constructs a typed create-task command sender backed by a Kafka sync producer.
func CreateKafkaSender(
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) task.CreateCommandSender {
	sender := cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)
	return task.NewCreateCommandSender(sender, "")
}

// CreateEscalator builds the auto-merge refusal escalator. It publishes
// `agent-escalation` notifications through the shared notification core on the
// same CQRS command topic the other producers use, so a refused PR reaches the
// operator's Telegram/Discord channels instead of sitting in a log line.
func CreateEscalator(
	ctx context.Context,
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) escalate.Escalator {
	sender := notification.NewNotificationPublishCommandSender(
		base.NewCommandCreator(base.RequestIDChannel(ctx)),
		cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory),
		cqrsiam.Initiator("watcher-github-pr"),
	)
	return escalate.NewEscalator(escalate.NewNotificationPublishFunc(sender))
}

// CreateWatcher wires all dependencies and returns a ready-to-use Watcher.
func CreateWatcher(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	cursorPath string,
	startTime libtime.DateTime,
	scope string,
	taskCreationFilter filter.TaskCreationFilter,
	sideEffectFilter filter.TaskCreationFilter,
	stage string,
	metrics pkg.Metrics,
	dispatchLedger pkg.DispatchLedger,
	trustDecision trust.Trust,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
	maxAdditions int,
	maxChangedFiles int,
	overrideLabel string,
	autoMergeLabel string,
	trivialAutoMergeEnabled bool,
	mechanicalOverrideAuthors []string,
	unreviewedConfig pkg.UnreviewedConfig,
	publishRecorder pkg.PublishRecorder,
	overrideLedger pkg.OverrideLedger,
	currentDateTime libtime.CurrentDateTimeGetter,
	coalescer coalesce.Coalescer,
	escalator escalate.Escalator,
) pkg.Watcher {
	// One tracker shared by publisher and watcher: the publisher notes each send.
	publishTracker := pkg.NewPublishTracker(currentDateTime)
	publisher := pkg.NewTaskPublisher(
		createSender,
		trustDecision,
		metrics,
		pkg.TaskConfig{
			Stage:           stage,
			MaxSlugLen:      maxSlugLen,
			MaxTitleLen:     maxTitleLen,
			TaskSuffix:      taskSuffix,
			TargetVault:     targetVault,
			MaxAdditions:    maxAdditions,
			MaxChangedFiles: maxChangedFiles,
		},
		ghClient,
		pkg.WithPublishRecorder(publishRecorder),
		pkg.WithOverrideLedger(overrideLedger),
		pkg.WithPublishTracker(publishTracker),
	)
	unreviewedScanner := pkg.NewUnreviewedScanner(
		ghClient, publisher, dispatchLedger, metrics, currentDateTime, scope,
		taskCreationFilter, trustDecision, overrideLabel,
		maxAdditions, maxChangedFiles, unreviewedConfig,
	)
	return pkg.NewWatcher(
		ghClient,
		publisher,
		metrics,
		cursorPath,
		startTime,
		scope,
		taskCreationFilter,
		sideEffectFilter,
		overrideLabel,
		autoMergeLabel,
		trivialAutoMergeEnabled,
		mechanicalOverrideAuthors,
		trustDecision,
		unreviewedScanner,
		coalescer,
		currentDateTime,
		pkg.WithEscalator(escalator),
		pkg.WithWatcherPublishTracker(publishTracker),
	)
}

// CreateTriggerPRReviewCommandSender constructs a typed trigger-PR-review
// command sender backed by a Kafka sync producer. This is the HTTP-side
// sender: the /trigger handler publishes TriggerPRReviewCommand messages
// through it.
//
// CommandCreator and Initiator are built once here and reused across every
// SendCommand call (per cqrs/docs/producing-commands.md "Factory Wiring";
// matches trading/frontend/command's reference impl).
func CreateTriggerPRReviewCommandSender(
	ctx context.Context,
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) command.TriggerPRReviewCommandSender {
	return command.NewTriggerPRReviewCommandSender(
		base.NewCommandCreator(base.RequestIDChannel(ctx)),
		cqrsiam.Initiator("watcher-github-pr"),
		cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory),
	)
}

// CreateSetDoNotMergeStatusCommandSender constructs a typed
// set-do-not-merge-status command sender backed by the same Kafka sync
// producer and topic trio as the trigger sender. The webhook receiver
// publishes through it on every delivery that can change the label state.
func CreateSetDoNotMergeStatusCommandSender(
	ctx context.Context,
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) command.SetDoNotMergeStatusCommandSender {
	return command.NewSetDoNotMergeStatusCommandSender(
		base.NewCommandCreator(base.RequestIDChannel(ctx)),
		cqrsiam.Initiator("watcher-github-pr"),
		cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory),
	)
}

// CreateSetDoNotMergeReviewCommandSender constructs a typed
// set-do-not-merge-review command sender backed by the same Kafka sync
// producer and topic trio as the trigger sender. The webhook receiver
// publishes through it on every delivery that can change the label state.
//
// This is the route that actually holds a PR: a CHANGES_REQUESTED review
// blocks the merge wherever the ruleset requires an approving review, and the
// reviewer App holds Pull requests: Write. The status route cannot work —
// the App declares no Commit statuses: Write.
func CreateSetDoNotMergeReviewCommandSender(
	ctx context.Context,
	syncProducer libkafka.SyncProducer,
	topicPrefix base.TopicPrefix,
) command.SetDoNotMergeReviewCommandSender {
	return command.NewSetDoNotMergeReviewCommandSender(
		base.NewCommandCreator(base.RequestIDChannel(ctx)),
		cqrsiam.Initiator("watcher-github-pr"),
		cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory),
	)
}

// CreateCommandConsumer wires a run.Func that consumes TriggerPRReviewCommand
// messages from the github-pr watcher's request topic and runs them through
// the single-PR review pipeline (GitHub fetch → filter → trust → publish).
//
// currentDateTime is the injected libtime.CurrentDateTimeGetter passed through
// to the trigger executor so it can derive a time-salted task identifier when
// the TriggerPRReviewCommand has Force=true (spec 067). The non-force path
// does not consult the clock.
//
// retryGuard is the process-wide in-flight guard consulted only for forced
// triggers.
//
// The function is pure composition: no business logic, no conditionals.
// It uses cdb.RunCommandConsumerTxDefault (auto-wraps the transaction) per
// the go-cqrs/auto-tx-wrapper-no-manual-wrap rule — do NOT manually wrap
// the executor with kv.NewTransactionMiddleware.
func CreateCommandConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	syncProducer libkafka.SyncProducer,
	db libkv.DB,
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
	topicPrefix base.TopicPrefix,
	currentDateTime libtime.CurrentDateTimeGetter,
) run.Func {
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTriggerPRReviewCommandExecutor(
			ghClient,
			createSender,
			taskCreationFilter,
			trustDecision,
			mechanicalOverrideAuthors,
			stage,
			maxSlugLen,
			maxTitleLen,
			taskSuffix,
			targetVault,
			maxAdditions,
			maxChangedFiles,
			metrics,
			retryGuard,
			overrideLedger,
			coalescer,
			currentDateTime,
		),
		// Posts the do-not-merge commit status the master-protection ruleset
		// requires, replacing the per-repo check-do-not-merge bridge
		// workflows. Rides the same topic trio as the trigger command; the
		// consumer dispatches by operation.
		command.NewSetDoNotMergeStatusCommandExecutor(ghClient),
		// Enforces the do-not-merge gate with a pull-request review — the
		// route the fleet actually uses, since the App cannot post the
		// equivalent commit status. Same topic trio; dispatched by operation.
		command.NewSetDoNotMergeReviewCommandExecutor(ghClient),
	}
	return cdb.RunCommandConsumerTxDefault(
		saramaClientProvider,
		syncProducer,
		db,
		lib.GithubPRReviewV1SchemaID,
		topicPrefix,
		false, // ignoreUnsupported
		executors,
	)
}
