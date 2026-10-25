---
status: completed
spec: [004-bug-retry-runs-alongside-original]
summary: Wired the RetryGuard into the forced executor path, factory and main, with executor-level AC1-AC6 specs
execution_id: github-pr-watcher-retry-inflight-guard-exec-012-retry-guard-executor-wiring
dark-factory-version: v0.198.7
created: "2026-10-25T17:22:00Z"
queued: "2026-10-25T17:43:15Z"
started: "2026-10-25T17:46:42Z"
completed: "2026-10-25T17:50:38Z"
branch: fix/retry-inflight-guard
---

# Wire the retry guard into the forced path, the factory and main

<!--
OPEN QUESTIONS FOR THE HUMAN REVIEWER (resolved in this prompt, flag at audit time):
1. `publishCreateCommand` is split: the identifier derivation moves into a new
   `deriveTaskID` helper so the executor holds the task identifier before publishing and
   can hand it to `RetryGuard.Record` afterwards. `DeriveTaskID` / `DeriveTaskIDForce`
   themselves are untouched (frozen by the spec).
2. The guard is constructed in `main.go` (it has `unreviewedConfig.Logins`, `ghClient`,
   `metrics` and `currentDateTime` in scope) and passed through
   `factory.CreateCommandConsumer` into the executor. The factory stays zero-logic.
3. AC1-AC4 are re-observed through the executor here, as the spec's decomposition asks;
   their unit-level assertions live in prompt 1.
4. Deliberate deviation from the spec's original transaction wording: the executor already
   runs inside `cdb.RunCommandConsumerTxDefault`'s `memDB.Update`, so the guard's
   `ListReviews` call runs INSIDE that already-held consumer transaction, exactly as
   `GetPRDetails` does today. The guard opens no transaction of its own and never calls
   `DB.Update`/`DB.View` (the `sync.RWMutex` is not re-entrant — a nested call deadlocks).
   The spec's transaction constraint is amended to match.
-->

<summary>
- A forced re-review now asks the guard before it publishes, and returns the guard's skip error unchanged when a review for that head SHA is already in flight.
- The unforced path never touches the guard: no ledger read, no verdict lookup, no new log line, no new counter.
- The published task identifier is recorded only after the create command was sent successfully, so a failed send leaves nothing behind and the next retry still publishes.
- A failed ledger write is logged and does not fail the command, because the command was already sent and a redelivery would publish a duplicate.
- The existing skip paths — invalid URL, filter skip, untrusted author and the webhook deferral — keep their current behaviour and ordering; the guard runs only after them.
- The guard is constructed once at startup from the existing reviewer-login config, threaded through the factory and injected into the executor, so the verdict half uses exactly the same login set as the unreviewed-PR scanner.
- An executor-level integration test drives a real ledger through two forced triggers, asserting one publish, a skip naming the first task, and the `inflight_skipped` counter.
- The unforced-path test asserts the guard was never consulted, so a guard that exists but is unreachable from the executor fails.
</summary>

<objective>
The running service consults one shared `pkg.RetryGuard` on every forced trigger: `main.go` builds it at startup, `factory.CreateCommandConsumer` passes it to `NewTriggerPRReviewCommandExecutor`, and `runTriggerPRReview` checks it before publishing and records the published task identifier after a successful send, all inside the consumer transaction it already holds. An executor-level test proves a second forced trigger on the same head SHA is skipped, and another proves the unforced path never reaches the guard.
</objective>

<context>
This prompt builds on prompt 1, which added `pkg/retry_guard.go` (`RetryGuard`, `RetryGuardPR`, `RetryGuardDecision`, `RetryGuardAction*`, `RetryGuardReason*`, `NewRetryGuard`), the `inflight_skipped` metric label and `mocks/retry_guard.go`. Read those first.

Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-cqrs.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before changing them:
- `pkg/command/trigger_pr_review_executor.go` — `NewTriggerPRReviewCommandExecutor`, `runTriggerPRReview` (note the `_ libkv.Tx` parameter), the webhook-deferral block (`if cmd.Webhook && !cmd.Force && slices.Contains(...)`), the `exclusion := pkg.ReviewIgnoreExclusion(...)` call, `publishCreateCommand` (the forced/`DeriveTaskIDForce` branch and the `createSender.SendCommand` error path), and the `SendResultEnabled = false` comment.
- `pkg/command/trigger_pr_review_executor_export_test.go` — the `RunTriggerPRReview` re-export and its compile-time signature guard.
- `pkg/command/trigger_pr_review_executor_test.go` — the eight `command.RunTriggerPRReview(...)` call sites (around lines 100, 197, 348, 387, 456, 487, 557 and 669), including the `Describe("force-true branch (spec 067)")` block, its `captureCreateCommand` closure, and the `metrics := new(mocks.Metrics)` / `IncPRPublishedArgsForCall(i)` label-collection pattern near line 667.
- `pkg/metrics_executor_test.go` — the `NewTriggerPRReviewCommandExecutor(...)` call site at the top of its `BeforeEach`.
- `pkg/factory/factory.go` — `CreateCommandConsumer` (its doc comment and the `command.NewTriggerPRReviewCommandExecutor(...)` call).
- `pkg/factory/integration_test.go` — the `NewTriggerPRReviewCommandExecutor(...)` call site and the `factory.CreateCommandConsumer(...)` call site.
- `pkg/factory/command_consumer_test.go` — its `factory.CreateCommandConsumer(...)` call site and the AST test that asserts the factory body has no control flow.
- `main.go` — `Run` (the `unreviewedConfig, err := pkg.ParseUnreviewedConfig(...)` block, `db := pkg.NewMemDB()`, and the `factory.CreateCommandConsumer(...)` call).
- `pkg/memdb.go` — `memDB.Update` / `View` hold one process-wide `sync.RWMutex` for the whole callback; `cdb.RunCommandConsumerTxDefault` wraps the executor in that `Update`, so the `tx` handed to the executor is already inside the lock.

Verified facts (do not re-invent):
- `libtime.DateTime` has `UnixMicro() int64`; `pkg.DeriveTaskIDForce(owner, repo string, number int, sha, nonce string) uuid.UUID`; `pkg.DeriveTaskID(owner, repo string, number int, sha string) uuid.UUID`.
- `unreviewedConfig.Logins` is `[]string` (`pkg.UnreviewedConfig`, already parsed in `main.go`).
- `mocks.RetryGuard` is the counterfeiter fake generated in prompt 1 (`CheckReturns`, `CheckCallCount`, `RecordReturns`, `RecordCallCount`, `RecordArgsForCall`).
- In the external `command_test` package there is no counter-reading helper (`readPRPublishedCounter` lives in `pkg/metrics_executor_test.go`, package `pkg_test`, and is not visible). Assert labels in `command_test` through a `*mocks.Metrics` fake by counting `metrics.IncPRPublishedArgsForCall(i) == "inflight_skipped"` over `metrics.IncPRPublishedCallCount()`; pass the same fake to `pkg.NewRetryGuard` so the guard's increment lands on it.
</context>

<requirements>
1. **Thread the guard through the executor constructor and the work function.** In `pkg/command/trigger_pr_review_executor.go`:

   - Add a parameter `retryGuard pkg.RetryGuard` to `NewTriggerPRReviewCommandExecutor`, immediately after `metrics pkg.Metrics` and before `currentDateTime libtime.CurrentDateTimeGetter`; pass it through to `runTriggerPRReview` in the same position.
   - Add the same parameter to `runTriggerPRReview`, same position.
   - Rename the unused second parameter from `_ libkv.Tx` to `tx libkv.Tx` (it is now used).
   - Update the constructor's doc comment: add a row to the exit-path mapping for the guard (`in-flight duplicate (forced) → cdb.ErrCommandObjectSkipped from the guard`) and extend the metric-label sentence (currently `create, skipped, kafka_error, trust_error`) so it lists every label the executor emits, including `deferred` and `inflight_skipped`.

2. **Consult the guard only on the forced path, after the existing skip paths.** Keep `runTriggerPRReview` under the `funlen` (80) and `gocognit` (20) limits by putting the guard logic in two small unexported helpers in the same file, each returning early when the command is not forced:

```go
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
```

   In `runTriggerPRReview`, immediately after the webhook-deferral `if cmd.Webhook && !cmd.Force && slices.Contains(...)` block and immediately before `exclusion := pkg.ReviewIgnoreExclusion(...)`, build the key and call the helper:

```go
	guardPR := pkg.RetryGuardPR{
		Owner:   prInfo.Owner,
		Repo:    prInfo.Repo,
		Number:  prInfo.Number,
		HeadSHA: details.HeadSHA,
	}
	if err := checkRetryGuard(ctx, tx, retryGuard, cmd, guardPR); err != nil {
		return nil, nil, err
	}
```

   Do not move, reorder or weaken the invalid-URL, filter, trust or webhook-deferral paths; the guard runs only after all of them have passed. Do not call the guard when `cmd.Force` is false — not `Check`, not `Record`, not the metric.

3. **Derive the task identifier once, before publishing, so it can be recorded afterwards.** Extract the derivation out of `publishCreateCommand` into a new unexported function in the same file, keeping the existing doc comment's rationale (title-path dedupe, prod incident `bborbe/coding#90`) attached to it:

```go
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
```

   In `runTriggerPRReview`, compute `taskIDStr := deriveTaskID(prInfo, cmd, details.HeadSHA, currentDateTime)` **after** the `checkRetryGuard` call (a skipped duplicate derives nothing) and pass it to `publishCreateCommand`. In `publishCreateCommand`, replace the `var taskIDStr string` / `if cmd.Force { ... } else { ... }` block with a `taskIDStr string` parameter and drop its now-unused `currentDateTime` parameter. The function must keep using `cmd.Force` for `pkg.BuildCreateCommand(...)` and `cmd.URL` in its error and log lines.

4. **Record only after a successful send, and never fail the command on a record error.** In `runTriggerPRReview`, after `publishCreateCommand` returns:

```go
	eventID, event, err := publishCreateCommand(
		ctx, prInfo, cmd, details, trustResult, createSender, taskIDStr,
		stage, maxSlugLen, maxTitleLen, taskSuffix, targetVault,
		maxAdditions, maxChangedFiles, metrics, exclusion,
	)
	if err != nil {
		return nil, nil, err
	}
	recordRetryGuard(ctx, tx, retryGuard, cmd, guardPR, taskIDStr)
	return eventID, event, nil
```

   with the second helper:

```go
// recordRetryGuard notes a successful forced publish in the guard's ledger.
// A record failure is logged, never returned: the create command was already
// sent, and surfacing an error would make the framework redeliver it.
func recordRetryGuard(
	ctx context.Context,
	tx libkv.Tx,
	retryGuard pkg.RetryGuard,
	cmd TriggerPRReviewCommand,
	guardPR pkg.RetryGuardPR,
	taskIDStr string,
) {
	if !cmd.Force {
		return
	}
	if err := retryGuard.Record(ctx, tx, guardPR, taskIDStr); err != nil {
		glog.Warningf(
			"retry guard: record publish failed pr=%s/%s#%d sha=%s task_id=%s err=%v",
			guardPR.Owner, guardPR.Repo, guardPR.Number, guardPR.HeadSHA, taskIDStr, err,
		)
	}
}
```

   Rationale to keep in that doc comment: a `Record` failure must not surface as an error, because the create command was already sent and the framework would redeliver it, publishing a duplicate. A failed send therefore leaves no record (the next forced trigger publishes), and a failed record write only widens the window in which one duplicate can slip through.

5. **Factory and `main.go`.**

   - `pkg/factory/factory.go`: add `retryGuard pkg.RetryGuard` to `CreateCommandConsumer` immediately after `metrics pkg.Metrics` and before `topicPrefix base.TopicPrefix`, pass it as the `retryGuard` argument to `command.NewTriggerPRReviewCommandExecutor`, and add one sentence to the function's doc comment: `retryGuard` is the process-wide in-flight guard consulted only for forced triggers. Keep the function pure composition — no logic, no conditionals.
   - `main.go`: in `Run`, next to `db := pkg.NewMemDB()`, construct the guard once at startup
     ```go
     retryGuard := pkg.NewRetryGuard(ghClient, metrics, unreviewedConfig.Logins, currentDateTime)
     ```
     and pass it to `factory.CreateCommandConsumer(...)` in the matching position. `unreviewedConfig`, `ghClient`, `metrics` and `currentDateTime` are all already in scope there.

6. **Update every existing call site so the package compiles.** Add a `retryGuard` argument to:
   - `pkg/command/trigger_pr_review_executor_export_test.go` — both the compile-time signature guard and the `RunTriggerPRReview` re-export call.
   - `pkg/command/trigger_pr_review_executor_test.go` — all eight `command.RunTriggerPRReview(...)` call sites (including the one inside the `captureCreateCommand` closure). Use `new(mocks.RetryGuard)` there: its zero-value `Check` returns a zero decision and nil error, so the existing non-forced and forced assertions are unchanged. Add the `"github.com/bborbe/github-pr-watcher/mocks"` import if missing.
   - `pkg/metrics_executor_test.go` — the `NewTriggerPRReviewCommandExecutor(...)` call site (around line 83; pass `new(mocks.RetryGuard)` between `metrics` and `libtime.NewCurrentDateTime()`).
   - `pkg/factory/integration_test.go` — both the `NewTriggerPRReviewCommandExecutor(...)` call site and the `factory.CreateCommandConsumer(...)` call site (`new(mocks.RetryGuard)` in both).
   - `pkg/factory/command_consumer_test.go` — the `factory.CreateCommandConsumer(...)` call site in the `Describe("CreateCommandConsumer")` block (`new(mocks.RetryGuard)`).

7. **Executor-level integration test.** Add a new `Describe` block to `pkg/command/trigger_pr_review_executor_test.go` (external `command_test` package) named for the retry guard, nested inside the existing `Describe("NewTriggerPRReviewCommandExecutor")` so that block's `BeforeEach` fixtures (`ctx`, `ghClient`, `createSender`, `taskCreationFilter`, `trustDecision`, and the `GetPRDetails` stub returning `HeadSHA: "abc123"`) apply. That block declares no clock, so the nested block declares its own `currentDateTime` (fake `libtime.CurrentDateTimeGetter` set to a non-zero instant, mutable per spec). Its own fixtures: `metrics := new(mocks.Metrics)` (passed to both the guard and the executor), a real `db := pkg.NewMemDB()`, a real `guard := pkg.NewRetryGuard(ghClient, metrics, []string{"octopus-pr-reviewer"}, currentDateTime)` with a mutable fake clock, the existing `ghClient` / `createSender` / `taskCreationFilter` / `trustDecision` fakes, a helper `inflightSkipped := func() int` that counts the indices `i < metrics.IncPRPublishedCallCount()` where `metrics.IncPRPublishedArgsForCall(i) == "inflight_skipped"`, and a helper that runs one forced command inside `db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error { _, _, err := command.RunTriggerPRReview(ctx, tx, newCommandObject(cmd), ghClient, createSender, taskCreationFilter, trustDecision, nil, "dev", 80, 200, "", "", 0, 0, metrics, guard, currentDateTime); return err })`. Cover:

   - **AC1 through the executor:** first forced trigger publishes (`SendCommandCallCount() == 1`, no error); second forced trigger at the same head SHA returns an error with `errors.Is(err, cdb.ErrCommandObjectSkipped)` true, the send count stays 1, `inflightSkipped()` grew by 1, and the error message contains the first trigger's published `TaskIdentifier` string.
   - **AC2 through the executor:** after recording a first publish and returning a review `{AuthorLogin: "octopus-pr-reviewer[bot]", CommitID: <headSHA>}` from `ListReviews`, a forced trigger publishes again (send count 2) with no error.
   - **AC3 through the executor:** a forced trigger for a head SHA with no record publishes exactly once and `inflightSkipped()` stays 0.
   - **AC4 through the executor:** advance the fake clock past one hour after the first publish; the next forced trigger publishes.
   - **AC6:** `createSender.SendCommandReturns(errors.Errorf(ctx, "kafka down"))` for the first forced trigger (assert the returned error is not `ErrCommandObjectSkipped` and `inflightSkipped()` did not move), then `SendCommandReturns(nil)` and run the same forced command again → it publishes (send count 2), proving no record was written by the failed send.
   - **AC5 negative half (unforced path untouched):** with a `*mocks.RetryGuard` fake injected instead of the real guard, run a non-forced command → `guard.CheckCallCount() == 0` and `guard.RecordCallCount() == 0`, and the send count is 1.
   - **AC5 positive half (wiring is reachable through the public executor):** with the `*mocks.RetryGuard` fake `guardFake`, run a forced command inside `db.Update` through `command.NewTriggerPRReviewCommandExecutor(ghClient, createSender, taskCreationFilter, trustDecision, nil, "dev", 80, 200, "", "", 0, 0, metrics, guardFake, currentDateTime).HandleCommand(ctx, tx, newCommandObject(cmd))` → `guardFake.CheckCallCount() == 1` and `guardFake.RecordCallCount() == 1`, and `_, _, _, taskID := guardFake.RecordArgsForCall(0)` equals the published `TaskIdentifier`.

   Add the `libkv "github.com/bborbe/kv"` import (not yet present in this test file) and the `"github.com/bborbe/github-pr-watcher/mocks"` import if missing; `"github.com/bborbe/github-pr-watcher/pkg"` is already imported.

8. **Do not add a CHANGELOG bullet** — prompt 1 already added the `## Unreleased` entry for this fix. Do not modify `pkg/taskid.go`, `pkg/filename.go`, the handlers, or the webhook flag.

9. **Self-check before finishing.** Walk each item and confirm it against the code you wrote, fixing any gap before ending:
   - AC1–AC6 each have a passing spec in the new `Describe` block.
   - With `Force == false`, `runTriggerPRReview` reaches neither `retryGuard.Check` nor `retryGuard.Record` (both helpers return first).
   - Neither the executor nor the guard calls `Update`/`View` on the DB.
   - A record failure is logged with `glog.Warningf` and never returned.
   - Re-run every `<verification>` command after `make precommit` and confirm each prints the stated count or exit status.
</requirements>

<constraints>
- **The unforced path is behaviourally unchanged:** no ledger read, no verdict lookup, no guard log line, no guard metric. The guard is not consulted at all when `Force` is false.
- **The guard never starts a ledger transaction of its own.** The executor already runs inside the consumer's `db.Update` (`cdb.RunCommandConsumerTxDefault`), so `ListReviews` runs inside that held transaction exactly as `GetPRDetails` does; never call `db.Update`/`db.View` from the guard or the executor (`memDB`'s `sync.RWMutex` is not re-entrant → deadlock). Deliberate deviation from the spec text (open question 4).
- **Do not weaken the existing skip paths** — invalid URL, filter skip and untrusted author keep their current behaviour and ordering; the webhook deferral (`cmd.Webhook && !cmd.Force`) stays exactly as it is.
- **The HTTP surface does not change.** `/trigger` keeps its 202 body `{"status","url","force"}` byte-identical; the handler learns nothing about task creation and must not wait for one.
- **Identifier derivation is frozen.** `DeriveTaskID` and `DeriveTaskIDForce` are not modified; the forced path keeps its time-salted identifier.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`.
- **No new environment variable, config field or dependency.** The reviewer set is `unreviewedConfig.Logins`.
- **Single replica is a precondition**, not something this change establishes.
- **Factory stays zero-logic** — parameters in, constructors out, no conditionals.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (with the new argument added at their call sites).
</constraints>

<verification>
Run `ROOTDIR=/workspace make test` — the full suite passes, including the new executor-level guard specs.

Run `ROOTDIR=/workspace make precommit` — format, lint, vet, generate and tests clean, exit 0.

Then confirm the wiring (each grep must match):

```bash
grep -n 'retryGuard.Check(ctx, tx, guardPR)' pkg/command/trigger_pr_review_executor.go   # exactly 1
grep -n 'retryGuard.Record(ctx, tx, guardPR, taskIDStr)' pkg/command/trigger_pr_review_executor.go   # exactly 1
grep -n 'pkg.NewRetryGuard' main.go                                                       # exactly 1
grep -c 'retryGuard,' pkg/factory/factory.go                                              # >= 1 (passed as an argument to the executor, not only declared)
grep -c 'checkRetryGuard(' pkg/command/trigger_pr_review_executor.go                       # exactly 2 (definition + call)
grep -c 'recordRetryGuard(' pkg/command/trigger_pr_review_executor.go                      # exactly 2 (definition + call)
! grep -qE '\.(Update|View)\(ctx' pkg/command/trigger_pr_review_executor.go                # exit 0 (no own transaction)
grep -c 'glog.Warningf' pkg/command/trigger_pr_review_executor.go                          # >= 1
grep -c 'func DeriveTaskIDForce(owner, repo string, number int, sha, nonce string) uuid.UUID' pkg/taskid.go   # exactly 1 (derivation frozen; the full diff check runs host-side per the spec)
```

`.git` is masked in the container (`hideGit=true`), so never use a `git` command here.

Compilation (`make test`) proves every `NewTriggerPRReviewCommandExecutor` / `RunTriggerPRReview` / `CreateCommandConsumer` call site carries the new argument.
</verification>
