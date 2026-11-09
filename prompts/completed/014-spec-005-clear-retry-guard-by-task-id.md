---
status: completed
spec: [005-bug-retry-guard-never-learns-outcome]
summary: Added RetryGuard.ClearByTaskID plus retryGuardOutcomeToken/splitRetryGuardKey, external and internal tests, regenerated the counterfeiter fake, and added the Unreleased changelog entry; make precommit exits 0.
execution_id: 6bd9c30e-d1b7-5dbd-8798-0f83b15e142a-exec-014-spec-005-clear-retry-guard-by-task-id
dark-factory-version: dev
created: "2026-11-09T18:04:24Z"
queued: "2026-11-09T18:26:24Z"
started: "2026-11-09T18:26:26Z"
completed: "2026-11-09T18:40:59Z"
branch: dark-factory/bug-retry-guard-never-learns-outcome
---

# Clear a retry-guard record by task identifier

<!--
OPEN QUESTIONS FOR THE HUMAN REVIEWER (resolved in this prompt, flag at audit time):
1. AC4 asks for "a table-driven test rendering the line for each outcome class". This
   repository has no glog-capture helper (AC3 records the same limitation), so the table
   asserts the class-dependent half — `retryGuardOutcomeToken` — for every outcome class,
   and the exact literal + argument order are enforced by the anchored grep in
   `<verification>` plus a compile-time look at the `glog.Infof` call. If the reviewer wants
   a literal end-to-end line assertion, that needs a log-capture helper this repo does not
   have.
2. `ClearByTaskID` deletes every record whose `TaskID` matches. In practice one task id is
   recorded once, but the scan-and-delete-all form is defensive and idempotent; the spec
   says "the matching record" (singular).
3. The `pr` and `sha` in the clear line are derived from the ledger key
   (`owner/repo#N@sha`) by splitting at the last `@`, rather than re-parsing owner / repo /
   number into typed fields. The key's left half is already exactly `owner/repo#N`, which is
   what the line needs.
4. AC6's `git diff origin/master -- pkg/retry_guard.go | grep retryGuardStaleAfter` check is
   host-side (`.git` is masked in the YOLO container), so this prompt asserts the bound is
   untouched with a container-runnable grep and by leaving the existing `Check` tests
   unchanged. The git diff stays on the spec's host-executable rung.
-->

<summary>
- The retry guard can now forget one published review when it is told that review's task identifier.
- Clearing scans the in-memory ledger and deletes only the record that names that task; every other pull request's record is untouched.
- A clear emits one operator-visible line at default verbosity naming the outcome, the pull request, the head SHA and the task.
- The outcome word is derived from the task's diagnostics (`failed`, `completed`, `skipped`), falling back to the terminal status, so a successful completion never reads `failed`.
- Clearing an identifier that matches no record is a silent no-op.
- The suppression window, the prune rule and the create/skip decision are unchanged; nothing about when a record is written changes either.
- The outcome-token derivation is unit-tested over every outcome class, so a hardcoded single token fails the suite.
- The guard's counterfeiter fake is regenerated so the new method is available to later wiring and tests.
- This prompt consumes no events yet; the consumer that calls the clear is the next prompt.
</summary>

<objective>
Extend `pkg.RetryGuard` with `ClearByTaskID(ctx, tx, taskID, outcome) (bool, error)`: it scans the in-memory ledger, deletes the record whose stored task identifier equals `taskID`, emits the fixed `retry guard cleared reason=task_outcome_<outcome> pr=<owner>/<repo>#<n> sha=<sha> task_id=<id>` line, and reports whether it cleared anything. Add the unexported `retryGuardOutcomeToken` helper that maps a diagnostics outcome and a terminal status to the token used in that line, and the unexported `splitRetryGuardKey` helper that derives `pr` and `sha` from a ledger key. This prompt covers AC1, AC3, AC4 and AC6; it does not wire a consumer.
</objective>

<context>
Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before changing them:
- `pkg/retry_guard.go` — `RetryGuard` (the interface and its doc comment), `retryGuard`, `Check`, `Record`, `prune`, `readRecord`, `retryGuardRecord` (`TaskID string` / `PublishedAt libtime.DateTime`), `retryGuardKey` (`[]byte(fmt.Sprintf("%s/%s#%d@%s", ...))`), `retryGuardBucketName`, and `retryGuardStaleAfter`.
- `pkg/memdb.go` — `memBucket.Iterator()` returns a real `memIterator`; `Item()` exposes `Key() []byte` and `Value(func([]byte) error) error`; the iterator snapshots its keys at creation.
- `pkg/retry_guard_test.go` — the existing `check` / `record` closures, the fake `libtime.CurrentDateTimeGetterFunc` clock, and the `Describe("RetryGuard")` fixtures to reuse.
- `pkg/unreviewed_scanner_internal_test.go` — the `package pkg` internal-test style (`Describe` / `DescribeTable` in package `pkg`, run by the `pkg_test` suite in `pkg/pkg_suite_test.go`).
- `mocks/retry_guard.go` — the generated fake; it is regenerated by `make generate`.
- `CHANGELOG.md` — the preamble and the top heading (`## v0.23.1`; there is no `## Unreleased`).

Verified library facts (read from the module cache; use these, do not re-invent):
- `libkv.Bucket.Iterator()` / `libkv.Iterator` expose `Rewind()`, `Valid()`, `Next()`, `Item()`, `Close()`; `libkv.Item` exposes `Key() []byte` and `Value(fn func([]byte) error) error`.
- `tx.Bucket(ctx, retryGuardBucketName)` returns `libkv.BucketNotFoundError` when the bucket does not exist (`errors.Is` matches it).
- `github.com/bborbe/errors` has `Is`, `Wrap(ctx, err, msg)` (returns nil for a nil error), `Wrapf(ctx, err, format, args...)` and `Errorf(ctx, format, args...)`.
- `retryGuardStaleAfter` is an untyped `time.Hour` constant (`const retryGuardStaleAfter = time.Hour`); do not change its type or value.
</context>

<requirements>
1. **Add `ClearByTaskID` to the `RetryGuard` interface** in `pkg/retry_guard.go`, with a doc comment stating that it scans the in-memory ledger for the record whose stored task identifier equals `taskID`, deletes it if present, logs the fixed clear line at default verbosity, and reports whether a record was deleted. Exact signature:

```go
ClearByTaskID(ctx context.Context, tx libkv.Tx, taskID string, outcome string) (bool, error)
```

   `outcome` is the pre-derived token (`failed`, `completed`, `skipped`, or a terminal status) used only in the log line; the caller derives it with `retryGuardOutcomeToken`. Leave the `Check` and `Record` signatures and doc comments unchanged.

2. **Implement `ClearByTaskID` on `*retryGuard`.** Exact behavior:

   - Read the bucket with `tx.Bucket(ctx, retryGuardBucketName)`. On `errors.Is(err, libkv.BucketNotFoundError)` return `(false, nil)`; any other error is returned wrapped (`errors.Wrap(ctx, err, "get retry guard bucket")`).
   - Iterate with `it := bucket.Iterator()`, `defer it.Close()`, `for it.Rewind(); it.Valid(); it.Next()`.
   - For each item, decode the value into `retryGuardRecord` with `item.Value(func(value []byte) error { return json.Unmarshal(value, &record) })`. A value that fails to decode cannot name a task, so skip it (do not return an error). Skip any record whose `record.TaskID != taskID`.
   - Collect the matching keys (copy them with `append([]byte(nil), item.Key()...)`) into a slice, then delete each after the iteration with `bucket.Delete(ctx, key)`; wrap a delete failure with `errors.Wrap(ctx, err, "delete retry guard record")`. Collect-then-delete keeps the operation safe for both the in-memory iterator and a future real store.
   - For every deleted key, derive `pr` and `sha` with `splitRetryGuardKey(string(key))` and emit exactly one line, at default verbosity:

```go
glog.Infof("retry guard cleared reason=task_outcome_%s pr=%s sha=%s task_id=%s",
	outcome, pr, sha, taskID)
```

   - Return `(len(keys) > 0, nil)`.
   - Do **not** call `tx.CreateBucketIfNotExists` (this is a read/delete path). Do **not** call `db.Update` or `db.View` inside `ClearByTaskID`; use only the `tx` handed in (the caller already holds the memDB write lock, which is not re-entrant). The unrelated `publishRecorder.RecordPublish` in the same file keeps its own `r.db.Update` — leave it as it is.

   The literal has **no colon after "guard"** — it is `retry guard cleared reason=task_outcome_`, unlike the existing `retry guard: …` lines. The verification grep is anchored at line start, so keep `glog.Infof("retry guard cleared reason=task_outcome_%s …` on one line; `make precommit` runs `golines --max-len=100`, which rewraps an over-long call and breaks the grep. Put the four arguments on the following line as shown.

3. **Add `retryGuardOutcomeToken`** (unexported, package `pkg`), with a doc comment naming the producer's three diagnostics values and the fallback:

```go
func retryGuardOutcomeToken(diagnosticsOutcome string, status string) string {
	switch diagnosticsOutcome {
	case "failed":
		return "failed"
	case "success":
		return "completed"
	case "skipped":
		return "skipped"
	default:
		return status
	}
}
```

   `status` is the terminal status string (`completed` or `aborted`). An unrecognized or absent diagnostics outcome falls back to `status`.

4. **Add `splitRetryGuardKey`** (unexported, package `pkg`):

```go
// splitRetryGuardKey splits a ledger key "<owner>/<repo>#<n>@<sha>" into the
// pull-request part and the head SHA. A key without '@' returns the whole key
// as the pull-request part and an empty SHA.
func splitRetryGuardKey(key string) (pr string, sha string) {
	if at := strings.LastIndex(key, "@"); at >= 0 {
		return key[:at], key[at+1:]
	}
	return key, ""
}
```

   Add the `strings` import to `pkg/retry_guard.go`. Keep `fmt` (still used by `retryGuardKey`).

5. **Do not change the suppression path.** `retryGuardStaleAfter`, `prune`, `Check`, `decideFresh`, `recordAge`, `retryGuardKey`, `readRecord`, and `Record` stay byte-for-byte except for the interface addition and the new methods/helpers. In particular the line `const retryGuardStaleAfter = time.Hour` must remain exactly one occurrence.

6. **Tests — external `pkg_test`, append to `pkg/retry_guard_test.go`** (or a new `pkg/retry_guard_clear_test.go` in `package pkg_test`). Reuse the existing `check` / `record` closures and the fake clock pattern. Cover at minimum:

   - **AC1:** record `task-1` for `pr`; call `ClearByTaskID` inside `db.Update(ctx, func(ctx, tx) { cleared, err = guard.ClearByTaskID(ctx, tx, "task-1", "completed") })`; assert `cleared == true`, no error, and a subsequent `check(pr)` returns `Action == create`, `Reason == "create"`, with `ghClient.ListReviewsCallCount() == 0` (the record is gone, so the verdict lookup is not reached).
   - **AC3:** record `task-1` for `pr`; clear `task-2`; assert `cleared == false`, no error, and a subsequent `check(pr)` (with `ghClient.ListReviewsReturns(nil, nil)`) returns `Reason == "skip_inflight"`.
   - **Only the matching record is cleared:** record `pr` → `task-1` and `otherPR` → `task-2`; clear `task-1`; `check(pr)` returns `create` and `check(otherPR)` returns `skip_inflight`.
   - **Idempotent:** a second clear of the same identifier returns `false`.
   - **Missing bucket:** `ClearByTaskID` on a fresh `pkg.NewMemDB()` (no record written) returns `(false, nil)`.
   - **AC6:** the existing `Check` tests (including the stale-record case) still pass unchanged; no new test asserts a different bound.

7. **Tests — internal `pkg`, new file `pkg/retry_guard_internal_test.go`** (`package pkg`), registered by the existing `pkg_test` suite. Add a `DescribeTable` over `retryGuardOutcomeToken` with at least these entries (a hardcoded single token must fail):

   | diagnosticsOutcome | status | want |
   |---|---|---|
   | `failed` | `completed` | `failed` |
   | `success` | `completed` | `completed` |
   | `skipped` | `completed` | `skipped` |
   | `""` | `aborted` | `aborted` |
   | `""` | `completed` | `completed` |
   | `unexpected` | `aborted` | `aborted` |

   Add a `DescribeTable` for `splitRetryGuardKey` with at least `bborbe/repo#42@abc123` → (`bborbe/repo#42`, `abc123`) and a key with no `@` → (whole key, `""`).

8. **Regenerate the counterfeiter fake.** Run `ROOTDIR=/workspace make generate` so `mocks/retry_guard.go` gains `ClearByTaskID` (with `ClearByTaskIDReturns`, `ClearByTaskIDCallCount`, `ClearByTaskIDArgsForCall`). Prompt 2's tests and the factory's existing `new(mocks.RetryGuard)` call sites depend on the fake satisfying the interface. Do not hand-edit the generated file.

9. **CHANGELOG.** `CHANGELOG.md` opens at `## v0.23.1`; there is no `## Unreleased`. Insert a new `## Unreleased` heading immediately above `## v0.23.1` (after the existing preamble block) with exactly one bullet describing the end state:

```
- fix: clear the retry guard's in-flight record when the review task reaches a terminal status, so a forced re-trigger after a failed review publishes instead of being silently suppressed for the full one-hour bound — the watcher consumes the prefixed task event stream, deletes the ledger record whose stored task identifier matches, and logs `retry guard cleared reason=task_outcome_<outcome> …`; non-terminal events and delete events leave the record in place, and the suppression window, prune and decision logic are unchanged
```

   Do not fold it into a released section. Prompt 2 must not add a second bullet.

10. **Self-check before finishing.** Walk each item against the code you wrote:
    - `ClearByTaskID` deletes only the matching record, logs one line per deleted record with the `task_outcome_` literal, and returns `true` only when it deleted something.
    - `retryGuardOutcomeToken` returns `failed` / `completed` / `skipped` for the producer's three diagnostics values and the terminal status otherwise.
    - `const retryGuardStaleAfter = time.Hour` appears exactly once and is untouched.
    - The `ClearByTaskID` method itself contains no `db.Update` / `db.View` call (the unrelated `publishRecorder.RecordPublish` keeps its own `r.db.Update`).
    - Re-run every `<verification>` command after `make precommit` and confirm each prints the stated count.
</requirements>

<constraints>
- **The suppression path is not bypassed.** `retryGuardStaleAfter`, the prune rule and the create/skip decision table are unchanged. This prompt only adds a way to delete a record earlier.
- **The ledger stays in-memory.** Do not introduce durable storage; a restart still clears every record.
- **The clear only uses the caller's transaction.** `ClearByTaskID` never opens its own `db.Update` / `db.View`; memDB's `sync.RWMutex` is not re-entrant, and the consumer that will call this method already holds the write lock.
- **Clearing is a no-op for an unknown identifier.** No record, no log line, no error.
- **The log line is one fixed literal at default verbosity** (`glog.Infof`, not `glog.V(2)`), shaped `retry guard cleared reason=task_outcome_<outcome> pr=<owner>/<repo>#<n> sha=<sha> task_id=<id>`; there is no colon after "guard".
- **Outcome tokens are derived, not hardcoded.** `failed`, `completed` and `skipped` come from the diagnostics outcome; anything else is the terminal status.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`.
- **No new dependency, config field or environment variable.** Only the stdlib `strings` import is added.
- **Do not touch the consumer, `main.go`, the factory, `pkg/taskid.go`, or the handlers** — the consumer is the next prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass (the regenerated fake must satisfy every existing `new(mocks.RetryGuard)` call site).
</constraints>

<verification>
Run, in this order:

```bash
ROOTDIR=/workspace make generate     # regenerates mocks/retry_guard.go with ClearByTaskID
ROOTDIR=/workspace make test         # full suite, including the new clear + token specs
ROOTDIR=/workspace make precommit    # ensure + format + generate + test + check, exit 0
```

Then confirm the observables (each must print the stated result):

```bash
grep -rnE '^\s*glog\.Infof\("retry guard cleared reason=task_outcome_' pkg/ --include='*.go'   # >= 1
grep -c 'retryGuardStaleAfter = time.Hour' pkg/retry_guard.go                                 # exactly 1 (bound unchanged)
grep -c 'ClearByTaskID' mocks/retry_guard.go                                                  # >= 1 (fake regenerated)
grep -c '^## Unreleased' CHANGELOG.md                                                         # exactly 1
grep -c 'func (g \*retryGuard) ClearByTaskID' pkg/retry_guard.go                              # exactly 1 (method added)
```

`.git` is masked in the container, so no `git` command runs here; the spec's `git diff … retryGuardStaleAfter` check stays on the host rung.
</verification>
