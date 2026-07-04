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
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
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

// CreateWatcher wires all dependencies and returns a ready-to-use Watcher.
func CreateWatcher(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	cursorPath string,
	startTime libtime.DateTime,
	scope string,
	taskCreationFilter filter.TaskCreationFilter,
	stage string,
	metrics pkg.Metrics,
	trustDecision trust.Trust,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
) pkg.Watcher {
	publisher := pkg.NewTaskPublisher(
		createSender,
		trustDecision,
		metrics,
		pkg.TaskConfig{
			Stage:       stage,
			MaxSlugLen:  maxSlugLen,
			MaxTitleLen: maxTitleLen,
			TaskSuffix:  taskSuffix,
		},
	)
	return pkg.NewWatcher(
		ghClient,
		publisher,
		metrics,
		cursorPath,
		startTime,
		scope,
		taskCreationFilter,
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

// CreateCommandConsumer wires a run.Func that consumes TriggerPRReviewCommand
// messages from the github-pr watcher's request topic and runs them through
// the single-PR review pipeline (GitHub fetch → filter → trust → publish).
//
// currentDateTime is the injected libtime.CurrentDateTimeGetter passed through
// to the trigger executor so it can derive a time-salted task identifier when
// the TriggerPRReviewCommand has Force=true (spec 067). The non-force path
// does not consult the clock.
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
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	metrics pkg.Metrics,
	topicPrefix base.TopicPrefix,
	currentDateTime libtime.CurrentDateTimeGetter,
) run.Func {
	executors := cdb.CommandObjectExecutorTxs{
		command.NewTriggerPRReviewCommandExecutor(
			ghClient,
			createSender,
			taskCreationFilter,
			trustDecision,
			stage,
			maxSlugLen,
			maxTitleLen,
			taskSuffix,
			metrics,
			currentDateTime,
		),
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
