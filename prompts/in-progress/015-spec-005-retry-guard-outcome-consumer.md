---
status: approved
spec: [005-bug-retry-guard-never-learns-outcome]
created: "2026-11-09T18:04:24Z"
queued: "2026-11-09T18:32:58Z"
branch: dark-factory/bug-retry-guard-never-learns-outcome
---

# Consume task lifecycle events and clear the retry-guard record

<!--
OPEN QUESTIONS FOR THE HUMAN REVIEWER (resolved in this prompt, flag at audit time):
1. Two packages are both named `lib`. `github.com/bborbe/agent` is package `lib` and holds
   `TaskV1SchemaID`, `Task`, `TaskContent` and `ParseMarkdown`; `github.com/bborbe/maintainer`
   is also package `lib` and holds `GithubPRReviewV1SchemaID`. `main.go` already binds `lib`
   to maintainer, so the consumer file imports the agent package as
   `agentlib "github.com/bborbe/agent"`. The spec's `lib.TaskV1SchemaID` means the agent one.
2. The spec requires the consumer to never return on a recoverable error. `libkafka` already
   recovers from per-partition errors internally, but its setup path (client creation,
   partition listing) returns an error. This prompt wraps `Consume` in a reconnect loop with
   one unexported fixed delay. No config knob is added — the spec's non-goals forbid tuning.
3. The `## Diagnostics` block is produced by `bborbe/github-pr-review-agent`, which is not in
   this repo or the module cache. The parser scans the section body line by line for a line
   whose trimmed form starts with `outcome:`, which handles both fenced YAML and a bare line,
   and trims surrounding quotes. If the producer ever indents the key under a list item this
   parser would miss it; the spec only ever names `outcome: failed` / `success` / `skipped`.
4. The event payload is decoded with `json.Unmarshal(msg.Value, &task)` (the same shape the
   notification consumer uses) and the task identifier is taken from `msg.Key`, because the
   sender puts `EventObject.ID` (= `base.EventID(task.TaskIdentifier)`) in the Kafka key and
   the event map in the value.
5. AC5's topic assertion needs the unexported `retryGuardOutcomeTopic`; it is tested in a
   `package pkg` internal test, matching `pkg/unreviewed_scanner_internal_test.go`.
-->

<summary>
- The watcher now watches the task lifecycle event stream alongside its poll loop, HTTP server and command consumer.
- When a task reaches `completed` or `aborted`, the guard record carrying that task's identifier is deleted, so the next forced trigger for that commit publishes instead of being suppressed.
- A task still `in_progress`, or at `next`, `backlog` or `hold`, leaves its record in place, so a genuinely running review keeps suppressing.
- A delete event, which carries no payload, is ignored quietly instead of being reported as a decode error.
- An event whose identifier matches no ledger record is a silent no-op.
- The consumer subscribes to the prefixed task event topic derived from the task schema, not a hardcoded name.
- The consumer runs as a fourth long-lived task and reconnects on recoverable errors rather than taking the other three down with it.
- The clear line it triggers names the diagnostics-derived outcome, the pull request, the head SHA and the task.
- The suppression window, the prune rule and the create/skip decision are unchanged.
</summary>

<objective>
Add `pkg.NewRetryGuardOutcomeMessageHandler` and `pkg.NewRetryGuardOutcomeConsumer`: the handler decodes one task event, ignores delete events and non-terminal statuses, derives the outcome token and calls `RetryGuard.ClearByTaskID`; the consumer is a `run.Func` that subscribes to `agentlib.TaskV1SchemaID.EventTopic(prefix)` and reconnects instead of returning on recoverable errors. Wire the consumer as the fourth `run.Func` in `main.go`. This prompt covers AC2, AC5 and AC7 and exercises AC1 through the consumer's own handler; it depends on prompt 1's `ClearByTaskID`, `retryGuardOutcomeToken` and regenerated fake.
</objective>

<context>
This prompt builds on prompt 1, which added `RetryGuard.ClearByTaskID`, `retryGuardOutcomeToken`, `splitRetryGuardKey`, the `retry guard cleared reason=task_outcome_…` literal and the regenerated `mocks/retry_guard.go`. Read those first.

Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cqrs.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before changing them:
- `pkg/retry_guard.go` — `RetryGuard.ClearByTaskID` (added by prompt 1), `retryGuardOutcomeToken`, `retryGuardRecord`, and the ledger's in-memory semantics.
- `pkg/retry_guard_test.go` — the `check` / `record` closures and fake clock to reuse in the consumer test.
- `pkg/factory/factory.go` — `CreateCommandConsumer` and its `cdb.RunCommandConsumerTxDefault` wiring; this is the consumer shape to mirror at the `libkafka` level.
- `main.go` — `Run`: `db := pkg.NewMemDB()`, `retryGuard := pkg.NewRetryGuard(...)`, `saramaClientProvider := libkafka.NewSaramaClientProviderNew(a.KafkaBrokers)`, `commandConsumer := factory.CreateCommandConsumer(...)`, and the final `run.CancelOnFirstFinish(ctx, pollLoop, httpServer, commandConsumer)`.
- `pkg/factory/command_consumer_test.go` — the `new(mocks.RetryGuard)` fixture and the factory no-control-flow AST test (only `CreateCommandConsumer` is checked; the new consumer lives in `pkg`).
- `pkg/unreviewed_scanner_internal_test.go` — the `package pkg` internal-test style for unexported helpers.

Verified library facts (read from the module cache; use these, do not re-invent):
- `agentlib.TaskV1SchemaID` is `cdb.SchemaID{Group: "agent", Kind: "task", Version: "v1"}`; `EventTopic(prefix base.TopicPrefix) libkafka.Topic` yields `develop-agent-task-v1-event` for prefix `develop` and `agent-task-v1-event` for an empty prefix.
- `agentlib.Task` embeds `base.Object[base.Identifier]` and has `TaskIdentifier TaskIdentifier`, `Frontmatter TaskFrontmatter`, `Content TaskContent`. `TaskFrontmatter.Status() domain.TaskStatus` reads `frontmatter["status"]`; `domain.TaskStatusCompleted` / `domain.TaskStatusAborted` are the terminal values.
- `agentlib.ParseMarkdown(ctx, content string) (*Markdown, error)` splits the body into `Section{Heading, Body}`; `Markdown.FindSection("## Diagnostics") (*Section, bool)`.
- `cdb.EventObject{Event base.Event, ID base.EventID, SchemaID cdb.SchemaID}`; `base.EventID.Bytes() []byte`; the sender publishes `json.Marshal(eventObject.Event)` as the value and `eventObject.ID` as the key.
- `libkafka.NewOffsetConsumerHighwaterMarksBatchWithProvider(provider, topic, offsetManager, batchHandler, batchSize, trigger, logSamplerFactory, options...) libkafka.Consumer`; `libkafka.NewStoreOffsetManager(libkafka.NewOffsetStore(db), libkafka.OffsetOldest, libkafka.OffsetNewest)`; `libkafka.NewMessageHandlerBatchTxUpdate(db, batchHandler)`; `libkafka.NewMessageHandlerBatchTx(handlerTx)`; `libkafka.NewMessageHandlerTxSkipErrors(handlerTx, log.DefaultSamplerFactory)` logs and swallows handler errors; `libkafka.MessageHandlerTx` has `ConsumeMessage(ctx, tx, msg) error`; `libkafka.MessageHandlerTxFunc` adapts a function; `libkafka.BatchSize(1)`; `run.NewTrigger()`.
- `sarama.ConsumerMessage{Key []byte, Value []byte}`; a tombstone/delete has `len(Value) == 0`.
</context>

<requirements>
1. **Create `pkg/retry_guard_consumer.go`** (`package pkg`, standard copyright header). Imports include `context`, `encoding/json`, `strings`, `time`, `github.com/IBM/sarama`, `agentlib "github.com/bborbe/agent"`, `base "github.com/bborbe/cqrs/base"`, `github.com/bborbe/errors`, `libkafka "github.com/bborbe/kafka"`, `libkv "github.com/bborbe/kv"`, `github.com/bborbe/log`, `github.com/bborbe/run`, `domain "github.com/bborbe/vault-cli/pkg/domain"`, `github.com/golang/glog`.

2. **Topic derivation (AC5).** Add:

```go
// retryGuardOutcomeTopic returns the prefixed task lifecycle event topic. The
// topic is derived from the schema id and the configured prefix; a bare literal
// would subscribe to the wrong topic under a prefix and receive nothing.
func retryGuardOutcomeTopic(prefix base.TopicPrefix) libkafka.Topic {
	return agentlib.TaskV1SchemaID.EventTopic(prefix)
}
```

3. **Terminal-status classification (AC2).** Add:

```go
// isRetryGuardTerminalStatus reports whether a task status ends the run.
// completed and aborted are terminal; in_progress, next, backlog and hold are
// not. hold is a pause with a scheduled resume, not an ending.
func isRetryGuardTerminalStatus(status domain.TaskStatus) bool {
	return status == domain.TaskStatusCompleted || status == domain.TaskStatusAborted
}
```

4. **Diagnostics outcome parsing (AC4).** Add:

```go
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
```

5. **The message handler (AC1, AC2, AC3, AC7).** Add:

```go
// NewRetryGuardOutcomeMessageHandler returns a transaction message handler that
// clears the retry guard record for a task that reached a terminal status.
//
// A delete event carries no payload and is ignored explicitly. A non-terminal
// status is ignored, so a running review keeps suppressing. The task identifier
// is the Kafka message key (the sender writes EventObject.ID, which is
// base.EventID(task.TaskIdentifier), there).
func NewRetryGuardOutcomeMessageHandler(guard RetryGuard) libkafka.MessageHandlerTx {
	return libkafka.MessageHandlerTxFunc(
		func(ctx context.Context, tx libkv.Tx, msg *sarama.ConsumerMessage) error {
			if len(msg.Value) == 0 {
				return nil
			}
			var task agentlib.Task
			if err := json.Unmarshal(msg.Value, &task); err != nil {
				return errors.Wrap(ctx, err, "decode task event")
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
```

6. **The consumer `run.Func` (the never-return constraint).** Add one unexported fixed reconnect delay and the constructor:

```go
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
					NewRetryGuardOutcomeMessageHandler(guard),
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
```

   Handler errors are already swallowed and logged by `NewMessageHandlerTxSkipErrors`, so they never propagate to `Consume`; the loop only covers setup/connection errors.

7. **Wire the fourth `run.Func` in `main.go`.** In `Run`, after `commandConsumer := factory.CreateCommandConsumer(...)`, add:

```go
	// Fourth run.Func: consumes task lifecycle events and clears the retry
	// guard record for a task that reached a terminal status, so a failed
	// review releases its suppression immediately instead of waiting out the
	// staleness bound.
	retryGuardOutcomeConsumer := pkg.NewRetryGuardOutcomeConsumer(
		saramaClientProvider,
		db,
		a.TopicPrefix,
		retryGuard,
	)
```

   Add `retryGuardOutcomeConsumer` as the fourth argument to the existing `run.CancelOnFirstFinish(ctx, a.runPollLoop(...), a.createHTTPServer(...), commandConsumer)`. Update the two nearby comments: the command-consumer comment (`// In-pod command consumer: third run.Func alongside poll + HTTP.`) and the order comment (`// Order: poll → HTTP → command consumer (spec 066 AC 9: three run.Funcs).`) so they say there are four `run.Func`s and name the outcome consumer as the fourth. `saramaClientProvider`, `db`, `a.TopicPrefix` and `retryGuard` are already in scope.

8. **Tests — external `pkg_test`, new `pkg/retry_guard_consumer_test.go`** (Ginkgo/Gomega; the suite is `pkg/pkg_suite_test.go`). Use a real `pkg.NewMemDB()` + real `pkg.NewRetryGuard(ghClient, pkg.NewMetrics(), []string{"octopus-pr-reviewer"}, fakeClock)` for the ledger assertions, and `new(mocks.RetryGuard)` where only the clear call matters. Build the message exactly as the sender does:

```go
func newTaskEventMessage(ctx context.Context, task agentlib.Task) *sarama.ConsumerMessage {
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
```

   Build tasks with a non-zero `base.Object[base.Identifier]{Identifier: base.Identifier(uuid.New().String()), Created: libtime.Now(), Modified: libtime.Now()}` (a zero `libtime.DateTime` marshals to `null`), `TaskIdentifier: agentlib.TaskIdentifier("<id>")`, `Frontmatter: agentlib.TaskFrontmatter{"status": "<status>"}`, and `Content` as needed. Invoke the handler as `handler.ConsumeMessage(ctx, tx, msg)` inside `db.Update(ctx, func(ctx, tx) error { ... })`. Cover at minimum:

   - **AC1 (through the handler, not by calling the clear):** record `task-1` for `pr`; feed a `completed` event for `task-1` whose content is `## Diagnostics\n\n```yaml\noutcome: failed\n```\n`; assert the subsequent `check(pr)` returns `Action == create`, `Reason == "create"`, and `ghClient.ListReviewsCallCount() == 0`.
   - **AC2 table over the six statuses.** For each of `completed`, `aborted`, `in_progress`, `next`, `backlog`, `hold`: with a `*mocks.RetryGuard`, feed an event carrying that status and assert `ClearByTaskIDCallCount()` is `1` for `completed`/`aborted` and `0` for the other four. Separately, with the real guard, record `task-1`, feed an `in_progress` event, and assert `check(pr)` still returns `Reason == "skip_inflight"` (with `ghClient.ListReviewsReturns(nil, nil)`).
   - **AC3:** record `task-1` for `pr`; feed a `completed` event for a different task id (`task-2`); assert the fake guard's `ClearByTaskIDCallCount() == 0` and, with the real guard, `check(pr)` still returns `skip_inflight`.
   - **AC4 token wiring through the handler:** with a fake guard, feed `completed` events with `outcome: failed` → `ClearByTaskIDArgsForCall(0)` outcome `failed`; `outcome: success` → `completed`; `outcome: skipped` → `skipped`; an `aborted` event with no diagnostics section → `aborted`. Assert the task-id argument equals the event key.
   - **AC5 decode:** the `newTaskEventMessage` helper builds a real `cdb.EventObject`; feeding it through the handler and asserting the fake guard receives the event's task identifier and the diagnostics-derived outcome proves the payload decodes back into a task.
   - **AC7 (in-flight still suppresses):** record `task-1`; feed only non-terminal events (`in_progress`, `next`, `backlog`, `hold`); assert `check(pr)` returns `Reason == "skip_inflight"`.
   - **Delete event:** feed `&sarama.ConsumerMessage{Key: []byte("task-1"), Value: nil}`; assert no error and `ClearByTaskIDCallCount() == 0`.
   - **Constructor:** `pkg.NewRetryGuardOutcomeConsumer(new(libkafkamocks.KafkaSaramaClientProvider), pkg.NewMemDB(), base.TopicPrefix("develop"), new(mocks.RetryGuard))` returns a non-nil `run.Func` (import `libkafkamocks "github.com/bborbe/kafka/mocks"`).

9. **Tests — internal `pkg`, new `pkg/retry_guard_consumer_internal_test.go`** (`package pkg`). Add:

   - `DescribeTable` for `retryGuardOutcomeTopic`: prefix `develop` → `develop-agent-task-v1-event`; prefix `""` → `agent-task-v1-event` (AC5).
   - `DescribeTable` for `retryGuardDiagnosticsOutcome`: `## Diagnostics\n\n```yaml\noutcome: failed\n```\n` → `failed`; `## Diagnostics\n\noutcome: success\n` → `success`; `## Diagnostics\n\n```yaml\noutcome: skipped\n```\n` → `skipped`; `## Result\n\noutcome: failed\n` → `""`; empty content → `""`.
   - `DescribeTable` for `isRetryGuardTerminalStatus`: `domain.TaskStatusCompleted` and `domain.TaskStatusAborted` → `true`; `domain.TaskStatusInProgress`, `domain.TaskStatusNext`, `domain.TaskStatusBacklog`, `domain.TaskStatusHold` → `false`.

10. **Do not add a second CHANGELOG bullet** — prompt 1 added the `## Unreleased` entry. Do not change the `RetryGuard` interface, `ClearByTaskID`, `retryGuardOutcomeToken`, `retryGuardStaleAfter`, the prune rule, the decision table, `pkg/taskid.go`, the handlers or the HTTP surface.

11. **Self-check before finishing.** Walk each item against the code you wrote:
    - A terminal event clears the matching record; a non-terminal event and a delete event do not.
    - The topic is `agentlib.TaskV1SchemaID.EventTopic(prefix)`, not a literal.
    - The consumer returns only when `ctx` is cancelled; a setup error logs and reconnects.
    - The handler and the consumer use the `tx` they are handed and open no transaction of their own.
    - Re-run every `<verification>` command after `make precommit` and confirm each prints the stated result.
</requirements>

<constraints>
- **Never clear on a non-terminal event.** The create event arrives moments after the record is written; clearing on it would disable the guard entirely. `in_progress`, `next`, `backlog` and `hold` leave the record in place. `hold` is a pause, not an ending.
- **A delete event is ignored explicitly** (`len(msg.Value) == 0`), not decoded and not logged as an error.
- **The topic is derived, never hardcoded:** `agentlib.TaskV1SchemaID.EventTopic(prefix)`. A bare `agent-task-v1-event` literal would silently receive nothing under a prefix.
- **The event key is the task identifier.** Use `string(msg.Key)`; the sender writes `base.EventID(task.TaskIdentifier)` there, the same value the ledger stores.
- **The status is read from `task.Frontmatter.Status()`**, not from a top-level event field. The terminal set is exactly `{completed, aborted}`.
- **The outcome token is derived from the diagnostics**, never hardcoded; a successful completion must not read `failed` in the log the operator greps.
- **The consumer never returns on a recoverable error.** `main.go` composes with `run.CancelOnFirstFinish`; a return cancels the poll loop, the HTTP server and the command consumer. It retries internally and returns only when `ctx` is cancelled.
- **Cross-vault events are expected and no filter is added.** Matching by task identifier makes them correct; the ledger is small.
- **The ledger stays in-memory.** A restart is still the only immediate way to clear a stuck record.
- **The suppression path is not bypassed.** `retryGuardStaleAfter`, the prune rule and the decision table are unchanged.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`.
- **No new config field, environment variable or tunable.** The reconnect delay is one unexported constant.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass; `make precommit`'s `go mod tidy` promotes the direct `sarama` and `vault-cli` imports.
</constraints>

<verification>
Run, in this order:

```bash
ROOTDIR=/workspace make test         # full suite, including the new consumer + handler specs
ROOTDIR=/workspace make precommit    # ensure + format + generate + test + check, exit 0
```

Then confirm the wiring and observables (each must print the stated result):

```bash
grep -rn 'TaskV1SchemaID' pkg/ --include='*.go'                            # >= 1 (topic derived, not a literal)
grep -rn 'retry guard cleared reason=task_outcome_' pkg/ --include='*.go'  # >= 1 (from prompt 1)
grep -c 'NewRetryGuardOutcomeConsumer' main.go                             # >= 1 (the wiring call)
grep -c 'retryGuardOutcomeConsumer,' main.go                               # >= 1 (passed to CancelOnFirstFinish)
grep -c 'run.CancelOnFirstFinish' main.go                                  # exactly 1
grep -c '^## Unreleased' CHANGELOG.md                                      # exactly 1 (prompt 1 added it; do not duplicate)
! grep -qE '\.(Update|View)\(ctx' pkg/retry_guard_consumer.go              # exit 0 (handler uses the caller's tx)
```

`.git` is masked in the container, so no `git` command runs here.
</verification>
