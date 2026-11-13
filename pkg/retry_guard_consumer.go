// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/IBM/sarama"
	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libkafka "github.com/bborbe/kafka"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	"github.com/golang/glog"
)

// retryGuardOutcomeTopic returns the prefixed task lifecycle event topic. The
// topic is derived from the schema id and the configured prefix; a bare literal
// would subscribe to the wrong topic under a prefix and receive nothing.
func retryGuardOutcomeTopic(prefix base.TopicPrefix) libkafka.Topic {
	return agentlib.TaskV1SchemaID.EventTopic(prefix)
}

// isRetryGuardTerminalStatus reports whether a task status ends the run.
// completed and aborted are terminal; in_progress, next, backlog and hold are
// not. hold is a pause with a scheduled resume, not an ending.
func isRetryGuardTerminalStatus(status domain.TaskStatus) bool {
	return status == domain.TaskStatusCompleted || status == domain.TaskStatusAborted
}

// retryGuardDiagnosticsOutcome returns the value of the outcome: line in the
// task's "## Diagnostics" section, or "" when the section or the line is
// absent. The producer writes failed, success or skipped.
func retryGuardDiagnosticsOutcome(ctx context.Context, content agentlib.TaskContent) string {
	doc, err := agentlib.ParseMarkdown(ctx, content.String())
	if err != nil {
		return ""
	}
	section, ok := doc.FindSection("## Diagnostics")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(section.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "outcome:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "outcome:"))
		return strings.Trim(value, `"'`)
	}
	return ""
}

// NewRetryGuardOutcomeMessageHandler returns a transaction message handler that
// clears the retry guard record for a task that reached a terminal status.
//
// A delete event carries no payload and is ignored explicitly. A non-terminal
// status is ignored, so a running review keeps suppressing. The task identifier
// is the Kafka message key (the sender writes EventObject.ID, which is
// base.EventID(task.TaskIdentifier), there).
func NewRetryGuardOutcomeMessageHandler(
	guard RetryGuard,
	ledger DispatchLedger,
) libkafka.MessageHandlerTx {
	return libkafka.MessageHandlerTxFunc(
		func(ctx context.Context, tx libkv.Tx, msg *sarama.ConsumerMessage) error {
			if len(msg.Value) == 0 {
				return nil
			}
			var task agentlib.Task
			if err := json.Unmarshal(msg.Value, &task); err != nil {
				return errors.Wrap(ctx, err, "decode task event")
			}
			// The dispatch mark comes BEFORE the terminal gate: a spawn notification
			// is not terminal, and it is the only event that proves the executor
			// picked the task up. job_started_at is written by the executor's spawn
			// notification and never by an agent delivery, so its presence is
			// exactly "a Job was spawned for this task".
			if startedAt, ok := task.Frontmatter.String("job_started_at"); ok && startedAt != "" {
				ledger.Mark(string(msg.Key))
			}
			status := task.Frontmatter.Status()
			if !isRetryGuardTerminalStatus(status) {
				return nil
			}
			outcome := retryGuardOutcomeToken(
				retryGuardDiagnosticsOutcome(ctx, task.Content),
				status.String(),
			)
			if _, err := guard.ClearByTaskID(ctx, tx, string(msg.Key), outcome); err != nil {
				return errors.Wrap(ctx, err, "clear retry guard record")
			}
			return nil
		},
	)
}

// retryGuardOutcomeReconnectDelay is how long the consumer waits before
// reconnecting after a recoverable error. The event topic is offset-tracked, so
// a reconnect resumes from the stored offset; nothing is lost. Deliberately not
// configurable: the spec adds no tuning knob.
const retryGuardOutcomeReconnectDelay = 5 * time.Second

// NewRetryGuardOutcomeConsumer returns a run.Func that consumes the task
// lifecycle event stream and clears the retry guard record for a task that
// reached a terminal status.
//
// It never returns on a recoverable error: main.go composes it with
// run.CancelOnFirstFinish, so a return would cancel the poll loop, the HTTP
// server and the command consumer. It retries internally and returns only when
// ctx is cancelled.
func NewRetryGuardOutcomeConsumer(
	saramaClientProvider libkafka.SaramaClientProvider,
	db libkv.DB,
	prefix base.TopicPrefix,
	guard RetryGuard,
	ledger DispatchLedger,
) run.Func {
	consumer := libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(
		saramaClientProvider,
		retryGuardOutcomeTopic(prefix),
		libkafka.NewStoreOffsetManager(
			libkafka.NewOffsetStore(db),
			libkafka.OffsetOldest,
			libkafka.OffsetNewest,
		),
		libkafka.NewMessageHandlerBatchTxUpdate(
			db,
			libkafka.NewMessageHandlerBatchTx(
				libkafka.NewMessageHandlerTxSkipErrors(
					NewRetryGuardOutcomeMessageHandler(guard, ledger),
					log.DefaultSamplerFactory,
				),
			),
		),
		libkafka.BatchSize(1),
		run.NewTrigger(),
		log.DefaultSamplerFactory,
	)
	return func(ctx context.Context) error {
		for {
			if err := consumer.Consume(ctx); err != nil && ctx.Err() == nil {
				glog.Warningf("retry guard outcome consumer: %v; reconnecting", err)
			}
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(retryGuardOutcomeReconnectDelay):
			}
		}
	}
}
