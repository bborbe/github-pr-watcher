---
status: completed
spec: [004-bug-retry-runs-alongside-original]
summary: Added pkg/retry_guard.go with the typed create-or-skip decision, in-memory pruned publish ledger, verdict lookup, three fixed log literals and inflight_skipped metric; implemented the real memDB iterator and IteratorTestSuite, generated the RetryGuard fake, and documented the change in CHANGELOG.
execution_id: github-pr-watcher-retry-inflight-guard-exec-011-retry-guard-ledger-and-decision
dark-factory-version: v0.198.7
created: "2026-10-25T17:22:00Z"
queued: "2026-10-25T17:43:13Z"
started: "2026-10-25T17:43:14Z"
completed: "2026-10-25T17:46:41Z"
branch: fix/retry-inflight-guard
---

# Add the retry guard: typed decision, in-memory publish ledger, verdict lookup

<!--
OPEN QUESTIONS FOR THE HUMAN REVIEWER (resolved in this prompt, flag at audit time):
1. The three log literals and the `inflight_skipped` metric increment are emitted from
   inside `pkg/retry_guard.go` (the guard owns the observable, driven by the same
   `Reason` value). Spec AC2 says "the caller logs one line"; the guard is the caller of
   `ListReviews`. Keeping log+metric next to the decision is what the spec's constraint
   "emitted from the same value, so the two cannot drift" asks for.
2. Pruning requires iterating the ledger bucket, but `pkg/memdb.go`'s `memIterator` is a
   stub (Iterator() returns an invalid iterator), so pruning would silently never run.
   This prompt therefore implements the real iterator in `pkg/memdb.go`. Nothing else in
   the repo uses it, so the change is contained.
3. AC1's evidence item (a) "the sender recorded zero sends" cannot be observed from the
   guard (it has no sender). It is asserted in prompt 2's executor-level test, which also
   re-observes AC1-AC4 through `runTriggerPRReview`. This prompt covers AC1's items
   (b) metric delta, (c) `errors.Is(err, cdb.ErrCommandObjectSkipped)` and (d) `Reason` +
   `ExistingTaskID`.
4. Deliberate deviation from the spec's original wording: the verdict lookup
   (`ListReviews`) runs INSIDE the transaction the caller hands the guard. The executor
   already runs inside `cdb.RunCommandConsumerTxDefault`'s `memDB.Update`, which holds the
   process-wide non-reentrant `sync.RWMutex`; `GetPRDetails` already calls GitHub there
   too. The guard therefore never opens a transaction of its own (never `DB.Update` /
   `DB.View` — that would deadlock) and uses only the `libkv.Tx` it is given. The spec's
   transaction constraint is amended to match.
-->

<summary>
- The watcher gains a new decision point that answers one question: may a forced re-review be published for this repository, pull request and head SHA?
- A review published less than an hour ago with no reviewer verdict at that head SHA is reported as already in flight, and the caller is told which task is running.
- A commit that already carries a verdict from a configured reviewer is allowed to re-run, and the decision says the verdict was overridden.
- A commit with no record at all publishes exactly as before, with no new log line.
- A record older than one hour stops suppressing, so a crashed run cannot block retries forever, and the decision carries the record's age.
- The record of what has been published lives in the watcher's existing in-process store and is pruned as new records are written, so it cannot grow without bound.
- A suppression moves a new `inflight_skipped` counter, distinct from the existing `skipped` label that means a filtered or untrusted pull request.
- A failed GitHub verdict lookup makes the guard permissive: it publishes rather than blocks, and logs the lookup failure.
- The one-hour ledger is lost when the watcher restarts; that limitation and the fact that a restart clears a stuck record are documented where the ledger is implemented.
- Nothing is wired into the executor yet; this prompt only adds and tests the guard.
</summary>

<objective>
When this prompt is done, package `pkg` contains a tested retry guard (`pkg/retry_guard.go`) that answers, for one repository, pull request and head SHA, whether a forced re-review may be published: it returns a typed create-or-skip decision with a reason, backed by an in-process ledger that is pruned on write and a reviewer-verdict lookup, and it emits the three fixed log literals and the `inflight_skipped` metric. `pkg/memdb.go`'s iterator really iterates, the counterfeiter fake exists, and `CHANGELOG.md` carries the `## Unreleased` bullet. The executor is unchanged; wiring is the next prompt.
</objective>

<context>
Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`

Read these source files fully before writing code:
- `pkg/unreviewed_scanner.go` — `matchesReviewerLogin` and `normalizeReviewerLogin` (the login-matching rule the guard must reuse, unexported, which is why the guard lives in package `pkg`), and the verdict loop in `qualify` (`review.CommitID == details.HeadSHA && matchesReviewerLogin(review.AuthorLogin, s.cfg.Logins)`).
- `pkg/githubclient.go` — the `GitHubClient` interface's `ListReviews(ctx, owner, repo string, number int) ([]Review, error)` and the `Review` struct's `AuthorLogin` / `CommitID` fields.
- `pkg/memdb.go` — `NewMemDB`, `memDB.Update` / `View` (a process-wide `sync.RWMutex` held for the whole callback), `memTx.Bucket` / `CreateBucketIfNotExists`, `memBucket`, and the stub `memIterator`.
- `pkg/metrics.go` — the `Metrics` interface and its `IncPRPublished` doc comment (the label list), and the `init()` pre-initialisation loop over `prPublishedTotal.WithLabelValues(...)`.
- `pkg/metrics_executor_test.go` — `readPRPublishedCounter` and `pkg.PRPublishedTotalForTest()` (how a label delta is asserted with `prometheus/testutil`).
- `pkg/taskid.go` — `DeriveTaskID` and `DeriveTaskIDForce` (frozen; this prompt does not touch them).
- `CHANGELOG.md` — the preamble and the top heading (`## v0.15.1`; there is no `## Unreleased`).

Verified library facts (read from the module cache; use these, do not re-invent):
- `github.com/bborbe/kv` `BucketName` is `[]byte`-based, so it can only be built as a `var`, never a `const`: `var retryGuardBucketName = libkv.BucketName("retry-guard-published")`.
- `libkv.Item` is `Exists() bool` / `Key() []byte` / `Value(fn func([]byte) error) error`; `libkv.NewByteItem(key, nil)` reports `Exists() == false`.
- `memTx.Bucket` returns `libkv.BucketNotFoundError` when the bucket does not exist.
- `libtime.DateTime` has `Sub(HasTime) libtime.Duration`, `libtime.Duration.Duration() time.Duration`, and JSON round-trip via `MarshalJSON` / `UnmarshalJSON`.
- `github.com/bborbe/errors` has `Is`, `Wrap(ctx, err, msg)` (returns nil for a nil error), `Wrapf(ctx, err, format, args...)` and `Errorf(ctx, format, args...)`.
- `cdb.ErrCommandObjectSkipped` is `github.com/bborbe/cqrs/cdb`'s skip sentinel (already used by `pkg/command/trigger_pr_review_executor.go`).
</context>

<requirements>
1. **Create `pkg/retry_guard.go`** (package `pkg`; the file must contain the words `in-memory` and `lost on restart` in its doc comment — the verification greps for them). Header comment matches every other file: `// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / `// Use of this source code is governed by a BSD-style` / `// license that can be found in the LICENSE file.`

   Types and constants, exactly these names and string values (the string values are the unit-level observable):

```go
// RetryGuardPR identifies one publishable review target.
type RetryGuardPR struct {
	Owner   string
	Repo    string
	Number  int
	HeadSHA string
}

// RetryGuardAction is what the guard wants the caller to do.
type RetryGuardAction string

const (
	RetryGuardActionCreate RetryGuardAction = "create"
	RetryGuardActionSkip   RetryGuardAction = "skip"
)

// RetryGuardReason is why the guard chose its action.
type RetryGuardReason string

const (
	RetryGuardReasonCreate                     RetryGuardReason = "create"
	RetryGuardReasonCreateForceOverrodeVerdict RetryGuardReason = "create_force_overrode_verdict"
	RetryGuardReasonCreateStaleRecordIgnored   RetryGuardReason = "create_stale_record_ignored"
	RetryGuardReasonSkipInflight               RetryGuardReason = "skip_inflight"
)

// RetryGuardDecision is the guard's verdict for one forced re-review.
type RetryGuardDecision struct {
	Action         RetryGuardAction
	Reason         RetryGuardReason
	ExistingTaskID string
	RecordAge      libtime.Duration
}

// retryGuardStaleAfter is how long a published record keeps suppressing a
// forced re-review. Prod pr-review Jobs carry activeDeadlineSeconds: 1800, so
// no legitimate run outlives this bound.
const retryGuardStaleAfter = time.Hour

var retryGuardBucketName = libkv.BucketName("retry-guard-published")

type retryGuardRecord struct {
	TaskID      string           `json:"task_id"`
	PublishedAt libtime.DateTime `json:"published_at"`
}
```

   `retryGuardStaleAfter` is an untyped `time.Hour` constant compared against `age.Duration()` (stdlib `time.Duration`). Do not make it a `libtime.Duration`.

2. **The interface and constructor.** Add the counterfeiter annotation and an interface with exactly two methods:

```go
//counterfeiter:generate -o ../mocks/retry_guard.go --fake-name RetryGuard . RetryGuard

// RetryGuard suppresses a forced re-review whose review for the same head SHA
// is still in flight. ...
type RetryGuard interface {
	Check(ctx context.Context, tx libkv.Tx, pr RetryGuardPR) (RetryGuardDecision, error)
	Record(ctx context.Context, tx libkv.Tx, pr RetryGuardPR, taskID string) error
}

func NewRetryGuard(
	ghClient GitHubClient,
	metrics Metrics,
	reviewerLogins []string,
	currentDateTime libtime.CurrentDateTimeGetter,
) RetryGuard {
	return &retryGuard{
		ghClient:        ghClient,
		metrics:         metrics,
		reviewerLogins:  reviewerLogins,
		currentDateTime: currentDateTime,
	}
}
```

   The interface doc comment must state, in plain words: the ledger is in-memory and lost on restart; a restart can only widen the window in which one duplicate slips through and is also the only immediate way to clear a stuck record; the verdict check is the restart-independent backstop; and the StatefulSet runs one replica so the ledger is not correct across replicas.

3. **`Check` — the three-way decision plus staleness.** Implement exactly this precedence, in this order:

   1. Read the ledger record for `(Owner, Repo, Number, HeadSHA)`: `bucket, err := tx.Bucket(ctx, retryGuardBucketName)`; `errors.Is(err, libkv.BucketNotFoundError)` means no record (any other error is returned wrapped). Then `item, err := bucket.Get(ctx, retryGuardKey(pr))`; `!item.Exists()` means no record. Never call `CreateBucketIfNotExists` in `Check` — it is a write. A value that fails `json.Unmarshal` makes `Check` return a wrapped error (the executor then fails the command loudly rather than guessing); `Record`'s prune loop deletes such undecodable values.
   2. No record → return `{Action: RetryGuardActionCreate, Reason: RetryGuardReasonCreate}`, no verdict lookup, no log line, no metric.
   3. Record present, compute `age := g.currentDateTime.Now().Sub(record.PublishedAt)`; clamp a negative age to `0` (wall clock jumped backwards — a future publish time must read fresh, never stale).
   4. `age.Duration() > retryGuardStaleAfter` → log the stale literal (requirement 5) and return `{Action: create, Reason: create_stale_record_ignored, RecordAge: age}`. No verdict lookup on this branch.
   5. Record fresh → call `g.ghClient.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number)`.
   6. Lookup error → log `glog.Warningf("retry guard: verdict lookup failed pr=%s/%s#%d err=%v", ...)` and return `{Action: create, Reason: create}` (permissive: an unavailable GitHub API can only make the guard allow, never block). Do not increment a metric.
   7. A review with `review.CommitID == pr.HeadSHA && matchesReviewerLogin(review.AuthorLogin, g.reviewerLogins)` exists → log the override literal and return `{Action: create, Reason: create_force_overrode_verdict}`. An empty reviewer set yields no match (that is the "no verdict known" behaviour).
   8. Otherwise → `g.metrics.IncPRPublished("inflight_skipped")`, log the suppression literal, and return `{Action: skip, Reason: skip_inflight, ExistingTaskID: record.TaskID, RecordAge: age}` together with an error that wraps `cdb.ErrCommandObjectSkipped` and names the existing task identifier:

```go
return RetryGuardDecision{...}, errors.Wrapf(
	ctx, cdb.ErrCommandObjectSkipped,
	"in-flight review already published pr=%s/%s#%d sha=%s task_id=%s",
	pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID,
)
```

   `Check` must never write to the ledger — only `Record` writes.

4. **`Record` — write after a successful publish, and prune on write.**

   - Open the bucket with `tx.CreateBucketIfNotExists(ctx, retryGuardBucketName)`.
   - Prune first: iterate the bucket (`bucket.Iterator()`, `defer it.Close()`, `for it.Rewind(); it.Valid(); it.Next()`), decode each value into `retryGuardRecord`, and `bucket.Delete(ctx, item.Key())` for every value that fails to decode and every record whose `now.Sub(record.PublishedAt).Duration() > retryGuardStaleAfter`.
   - Then `json.Marshal(retryGuardRecord{TaskID: taskID, PublishedAt: now})` and `bucket.Put(ctx, retryGuardKey(pr), value)`.
   - Capture `now := g.currentDateTime.Now()` once at the top and use it for both pruning and the written timestamp.
   - `retryGuardKey(pr)` is `[]byte(fmt.Sprintf("%s/%s#%d@%s", pr.Owner, pr.Repo, pr.Number, pr.HeadSHA))`.

5. **The three fixed log literals, at default verbosity, each at the start of a `glog.Infof` call.** Exact prefixes (the verification greps are anchored at line start):

```go
ageSec := int64(age.Duration() / time.Second)
glog.Infof("retry guard: suppressed in-flight run pr=%s/%s#%d sha=%s"+
	" task_id=%s age_seconds=%d",
	pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID, ageSec)
glog.Infof("retry guard: force overrode verdict pr=%s/%s#%d sha=%s",
	pr.Owner, pr.Repo, pr.Number, pr.HeadSHA)
glog.Infof("retry guard: stale in-flight record ignored pr=%s/%s#%d sha=%s"+
	" task_id=%s age_seconds=%d",
	pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, record.TaskID, ageSec)
```

   Hoist the age conversion into a local (`ageSec`) so no argument line approaches the limit — golines rewraps the WHOLE call when any of its lines is too long.

   **Layout is load-bearing.** The opening `glog.Infof("retry guard: <literal>` must stay on ONE line, because the verification greps are anchored `^\s*glog\.Infof\("retry guard: …`. `make precommit` runs `golines --max-len=100` (tab width 4), which rewraps any line over 100 columns into `glog.Infof(` / `"…",` on separate lines and breaks the grep. Keep every line of these three calls ≤ 100 columns at their final indentation — split the format string with `+` concatenation as shown, never put the literal on its own line. If an extracted helper puts the call deeper, shorten the first fragment rather than re-wrapping. Re-run the three greps AFTER `make precommit`, not before.

   Do not use `glog.V(2)` for these three; the deployment may not set `-v`. The lookup-error line in requirement 3.6 may be `glog.Warningf`.

6. **Make the ledger prunable — implement the real iterator in `pkg/memdb.go`.** `memBucket.Iterator()` and `memBucket.IteratorReverse()` currently return `&memIterator{}` whose `Valid()` is always false, so pruning would never delete anything. Replace the stub with:

```go
type memIterator struct {
	keys    []string
	values  map[string][]byte
	reverse bool
	index   int
}

func newMemIterator(values map[string][]byte, reverse bool) libkv.Iterator {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if reverse {
		slices.Reverse(keys)
	}
	return &memIterator{keys: keys, values: values, reverse: reverse}
}

func (i *memIterator) Close() {}

func (i *memIterator) Item() libkv.Item {
	if !i.Valid() {
		return libkv.NewByteItem(nil, nil)
	}
	key := i.keys[i.index]
	return libkv.NewByteItem([]byte(key), i.values[key])
}

func (i *memIterator) Next() { i.index++ }

func (i *memIterator) Valid() bool { return i.index >= 0 && i.index < len(i.keys) }

func (i *memIterator) Rewind() { i.index = 0 }

// Seek positions at the first key >= key (forward) or <= key (reverse).
func (i *memIterator) Seek(key []byte) {
	k := string(key)
	i.index = sort.Search(len(i.keys), func(n int) bool {
		if i.reverse {
			return i.keys[n] <= k
		}
		return i.keys[n] >= k
	})
}
```

   `Rewind()` positions at the first element (index 0) — callers use `for it.Rewind(); it.Valid(); it.Next()`, so `-1` would skip nothing yet report invalid and the loop would never run. The zero value of `index` is therefore also "rewound".

   `memBucket.Iterator()` returns `newMemIterator(b.b, false)`, `memBucket.IteratorReverse()` returns `newMemIterator(b.b, true)`. Add the `slices` import to `pkg/memdb.go` if missing. Update the `NewMemDB` doc comment so it no longer says iteration is unsupported (the iterator is now real). Do not change any other method of `memDB` / `memBucket`.

   Add `pkg/memdb_test.go` (package `pkg_test`) that runs the library's contract suite against the real store, inside a `Describe("memDB", …)`:

```go
libkv.IteratorTestSuite(libkv.ProviderFunc(func(ctx context.Context) (libkv.DB, error) {
	return pkg.NewMemDB(), nil
}))
```

   It must pass, including the reverse-`Seek` cases. Exemplar for semantics: `memorykv_iterator.go` in the module cache (`$(go env GOMODCACHE)/github.com/bborbe/memorykv@*/memorykv_iterator.go`) and the suite in `$(go env GOMODCACHE)/github.com/bborbe/kv@*/kv_iterator-test-suite.go` — read both before writing the iterator; if the suite's expectations differ from the code above, the suite wins.

7. **Register the metric label in both places in `pkg/metrics.go`.** Add `"inflight_skipped"` (a) to the `IncPRPublished` doc comment's label list (on its own line so the file contains the quoted literal exactly twice) and (b) to the `init()` loop's `[]string{...}` for `prPublishedTotal.WithLabelValues(cmd).Add(0)` — the loop already carries fourteen values and the comment lists seven; add to each list independently, do not replace one with the other. Do not reuse `skipped`.

8. **Tests — new file `pkg/retry_guard_test.go`, external package `pkg_test`, Ginkgo/Gomega** (the suite already exists in `pkg/pkg_suite_test.go`). Build a real ledger with `pkg.NewMemDB()`, a real guard with `pkg.NewRetryGuard(ghClient, pkg.NewMetrics(), logins, currentDateTime)`, a `*mocks.GitHubClient` fake, and a mutable fake clock fixed at a non-zero instant — a zero `DateTime` marshals to `null` and every record would then read stale (`libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return fakeNow })`). Run each guard call inside `db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error { ... })` because `Check`/`Record` take the executor's transaction. Cover, at minimum:

   - **AC1** fresh record, no verdict → `Check` returns `Action == skip`, `Reason == "skip_inflight"`, `ExistingTaskID` equal to the recorded identifier, `errors.Is(err, cdb.ErrCommandObjectSkipped)` true, and `readPRPublishedCounter("inflight_skipped")` grew by exactly 1 (read it via `pkg.PRPublishedTotalForTest()` + `prometheus/testutil`, snapshotting before/after as `pkg/metrics_executor_test.go` does).
   - **AC2** fresh record plus a review `{AuthorLogin: "octopus-pr-reviewer[bot]", CommitID: <headSHA>}` with `logins = []string{"octopus-pr-reviewer"}` → `Action == create`, `Reason == "create_force_overrode_verdict"`, no error, `inflight_skipped` unchanged. Also assert the `[bot]`-suffix normalisation by using a different casing.
   - **AC3** no record at all → `Action == create`, `Reason == "create"` (not `create_force_overrode_verdict`, not `create_stale_record_ignored`), and `ghClient.ListReviewsCallCount() == 0`.
   - **AC4** record written at T, clock advanced 61 minutes → `Action == create`, `Reason == "create_stale_record_ignored"`, `RecordAge` between 60 and 62 minutes.
   - **AC7** `Check` → create, `Record(..., "task-1")`, `Check` again (same clock) → skip naming `task-1`.
   - **AC6 counterpart** two consecutive `Check` calls with no `Record` in between both return create (proves `Check` writes nothing).
   - **Pruning** `Record` key A at T, advance 2 hours, `Record` key B, then `Check` key A → `Reason == "create"` (no record), not `create_stale_record_ignored` — this distinguishes pruning from staleness and fails if the `memDB` iterator is still a stub.
   - **Negative age clamp** record at T, clock set to T−5m → skip (fresh), not `create_stale_record_ignored`.
   - **Lookup error** fresh record + `ghClient.ListReviewsReturns(nil, errors.Errorf(ctx, "github down"))` → `Action == create`, `Reason == "create"`, no error, `inflight_skipped` unchanged.
   - **Empty reviewer set** fresh record + a review at the head SHA, `logins = nil` → skip (no verdict known).
   - **Verdict at a different SHA** fresh record, review `CommitID` differs from the head SHA → skip.
   - **Different head SHA** record for SHA A, `Check` for SHA B → `Reason == "create"` (records are keyed per head SHA).

   Test the boundary that matters: every guard call goes through the real `libkv` transaction against `pkg.NewMemDB()` — no in-memory map shortcut in the test.

9. **Generate the counterfeiter fake.** Run `ROOTDIR=/workspace make generate` so `mocks/retry_guard.go` exists in the working tree with `--fake-name RetryGuard` (prompt 2 depends on it). `make precommit` regenerates mocks as well.

10. **CHANGELOG.** `CHANGELOG.md` opens directly at `## v0.15.1`; add a new `## Unreleased` heading above it with exactly one bullet:

```
- fix: suppress a forced re-review while a review for the same head SHA is still in flight, so an operator retry no longer publishes a second task alongside the running one — the forced path consults an in-memory ledger of published reviews plus a reviewer-verdict lookup, counts a suppression under the new `inflight_skipped` label on `github_pr_watcher_prs_total`, and still re-runs after a verdict or once the one-hour staleness bound passes
```

   Do not fold the bullet into a released section.

11. **Self-check before finishing.** Walk each case against the code you wrote, not the tests alone, and fix any mismatch:
    - AC1: fresh record + no verdict → skip, `skip_inflight`, task id named, error wraps `cdb.ErrCommandObjectSkipped`, `inflight_skipped` +1, suppressed-literal logged.
    - AC2: fresh record + verdict at head SHA → create, `create_force_overrode_verdict`, override literal logged, no metric move.
    - AC3: no record → create, `create`, no `ListReviews` call, no guard log line.
    - AC4: record older than one hour → create, `create_stale_record_ignored`, stale literal logged with age.
    - AC7: `Record` after a create makes the next `Check` skip with that task id.
    - Pruning: a `Record` deletes every record older than one hour and every undecodable value; `memIterator` really iterates (forward and reverse) and `pkg/memdb_test.go` runs `libkv.IteratorTestSuite` green.
    - No `DB.Update` / `DB.View` call anywhere in `pkg/retry_guard.go`.
    - Re-run every `<verification>` command after `make precommit` and confirm each prints the stated result.
</requirements>

<constraints>
- **The reviewer set is the existing one.** Reuse `matchesReviewerLogin` and the configured logins passed in; no new environment variable and no new config field.
- **No new external state.** The ledger lives in the existing in-process key-value store, keyed on repository, pull request and head SHA. No new Kafka topic, no new schema, no new dependency, no PVC.
- **The ledger is in-memory and lost on restart, and that is acceptable.** State this in the new code's doc comment, together with the fact that a restart is also the only immediate way to clear a stuck record.
- **The staleness bound is one hour**, measured from the record's publish time.
- **Three fixed log literals**, each via `glog.Infof` at default verbosity (not `glog.V(2)`), each carrying the head SHA; the first and third also carry the task identifier and the age in seconds.
- **The metric label is registered in two places** (doc comment and `init()` loop) and must not reuse `skipped`.
- **The guard lives in package `pkg`** — `matchesReviewerLogin` is unexported.
- **The guard never starts a transaction of its own.** The verdict lookup runs inside the caller's already-held transaction, exactly as `GetPRDetails` already does in the executor (deliberate deviation from the spec's earlier wording — open question 4). `Check`/`Record` only use the `libkv.Tx` they are handed and must never call `DB.Update`/`DB.View` themselves: `memDB.Update` holds a process-wide non-reentrant `sync.RWMutex` for the whole callback, so a nested call would deadlock under the consumer's held lock.
- **The ledger is pruned on write**, so the map cannot grow without bound over the process lifetime.
- **`force overrode verdict` means an override.** That reason and its log line are emitted only when a record exists *and* a verdict exists at the head SHA. A verdicted SHA with no surviving record publishes with `Reason == "create"` and no guard log line.
- **Identifier derivation is frozen.** Do not modify `DeriveTaskID` or `DeriveTaskIDForce`.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`, no `context.Background()` in package code.
- **Do not touch the executor, the factory, `main.go`, or any handler** — that is the next prompt.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `ROOTDIR=/workspace make test` — the full suite passes, including the new `pkg/retry_guard_test.go` specs.

Run `ROOTDIR=/workspace make precommit` — format, lint, vet, generate and tests clean, exit 0.

Then confirm the AC-specific observables (each must print a non-empty result / the stated count):

```bash
grep -rnE '^\s*glog\.Infof\("retry guard: force overrode verdict' pkg/ --include='*.go'
grep -rnE '^\s*glog\.Infof\("retry guard: stale in-flight record ignored' pkg/ --include='*.go'
grep -rnE '^\s*glog\.Infof\("retry guard: suppressed in-flight run' pkg/ --include='*.go'
grep -c '"inflight_skipped"' pkg/metrics.go          # must print 2
grep -c '^## Unreleased' CHANGELOG.md                # must print 1
grep -c 'lost on restart' pkg/retry_guard.go        # must print >= 1
grep -c 'in-memory' pkg/retry_guard.go              # must print >= 1
! grep -qE '\.(Update|View)\(ctx' pkg/retry_guard.go  # must exit 0 (no own transaction)
ls mocks/retry_guard.go
```
</verification>
