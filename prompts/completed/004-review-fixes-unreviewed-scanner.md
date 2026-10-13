---
status: completed
summary: 'Hardened the unreviewed-PR scanner: trust-check errors now abort the scan instead of publishing a partial gauge, confirmed-reviewed PRs are cached until their UpdatedAt moves, overlapping scans are skipped via TryLock, and the Poll→Scan wiring is test-guarded.'
execution_id: github-pr-watcher-unreviewed-gauge-exec-004-review-fixes-unreviewed-scanner
dark-factory-version: v0.198.7
created: "2026-10-13T11:08:11Z"
queued: "2026-10-13T11:08:11Z"
started: "2026-10-13T11:10:28Z"
completed: "2026-10-13T11:17:24Z"
---

# Harden the unreviewed scanner: no partial publish on trust errors, cache settled reviews, test the Poll wiring

<summary>
- A failure to check whether a pull request's author is trusted now aborts the whole scan, like every other lookup failure, so the gauge never loses a series because of a transient error
- Pull requests whose current commit the reviewer bot has already reviewed are remembered, so the scanner stops re-fetching their details and reviews on every poll until they change
- The poll cycle's call into the scanner is now covered by tests that fail if the call is removed or happens on an aborted poll
- An overlapping manual check no longer queues a second full scan behind the first one; it simply skips
- Small cleanups: a stale comment, an empty-result heartbeat log, an error-wrapping call, and the waiting-time setting is only validated when the feature is switched on
</summary>

<objective>
Address the local review of the unreviewed-PR scanner (added by the two previous prompts) before the PR goes to the bot reviewer: make trust-check errors fail the scan instead of producing a partial gauge, stop paying two GitHub calls per already-reviewed PR on every poll (the scanner runs on the shared App token that was exhausted once on 2026-08-23), and make the Poll→Scan wiring test-guarded.
</objective>

<context>
Read CLAUDE.md if present, and `docs/dod.md`.

Read before writing:
- `pkg/unreviewed_scanner.go` — the scanner as it is now: `Scan`, the per-PR qualify helper, the trust-error branch (currently "log and skip, scan still publishes"), the retry state map + `prKey` pruning, the `sync.Mutex`, `ParseUnreviewedConfig`.
- `pkg/unreviewed_scanner_test.go` — the existing scanner tests, including the trust-error case that currently asserts the scan still publishes, and the retry-budget / pruning / concurrency tests.
- `pkg/watcher.go` — `Poll` and its call to `w.unreviewedScanner.Scan(ctx)`; the early-return branch when `fetchAllPRs` aborts.
- `pkg/watcher_test.go` — `newTestWatcher*` helpers and `newDisabledUnreviewedScanner()`; `mocks/unreviewed_scanner.go` (generated, currently unused).
- `pkg/metrics.go` — the comment inside `(m *prometheusMetrics) SetUnreviewedPRs` ("A late-finishing older scan …").
- `pkg/githubclient.go` — `PullRequest.UpdatedAt`.
</context>

<requirements>
1. **Trust error aborts the scan.** In the scanner, a `trustDecision.IsTrusted` error is now handled exactly like a `GetPRDetails` / `ListReviews` error: `glog.Warningf` and return WITHOUT calling `SetUnreviewedPRs` (no partial set, timestamp not advanced) and without any retry this cycle. Retry state is untouched. Update the existing trust-error test accordingly (it must now assert `SetUnreviewedPRsCallCount() == 0` and no `PublishCreate`), keep the "entry not pruned across a trust error" retry test passing, rename that test to "aborts the scan when a trust check errors" and make it mixed (first PR's trust check errors, second PR trusted) so it proves no partial set is published. In "does not reset the budget when the trust check errors" add `Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(1))` after the failing scan. In `Scan`, change the prune comment "Any other skip — threshold, filter, trust error, empty SHA, park, a review arriving —" to drop "trust error" (trust errors now abort before pruning), and add trust errors to the aborting errors in the `qualify` doc comment. README already says a failed scan leaves the previous gauge in place; change README only if it says otherwise.

2. **Cache settled reviews.** Add, under the existing scanner mutex, a map keyed by `prKey` → `updatedAt libtime.DateTime` recording PRs whose head SHA was confirmed reviewed by a configured login. In `qualify`, immediately after the threshold check and before `GetPRDetails`: if the PR's `UpdatedAt` equals the cached value, return not-counted WITHOUT calling `GetPRDetails` or `ListReviews`; on a mismatch delete the entry first. Write the entry only in the matching-review branch (a review by a configured login with `CommitID == details.HeadSHA`). Extract a small helper (e.g. `cachedReviewed`) if `qualify` approaches the gocognit/funlen caps (any push, draft toggle, comment or label moves `UpdatedAt`, so a change always re-checks). Prune entries for PRs absent from the full `SearchOpenPRs` result, alongside the retry-state pruning. Never cache a PR that was counted as unreviewed, skipped, or errored. Tests:
   - first scan: reviewed PR ⇒ `GetPRDetails` + `ListReviews` each called once; second scan with identical `UpdatedAt` ⇒ neither called again, PR still not counted
   - second scan with `UpdatedAt` moved ⇒ both called again
   - an unreviewed PR is never cached (its calls repeat every scan)
   - PR absent from one scan's search result, then back with the same `UpdatedAt` ⇒ calls happen again (entry pruned)
   - document in the README `## Metrics` section that settled PRs cost no calls until they change

3. **Poll→Scan wiring tests.** In `pkg/watcher_test.go`, using `mocks.UnreviewedScanner`:
   - a successful `Poll` calls `Scan` exactly once
   - a `Poll` whose main search fails (`ghClient.SearchPRsReturns(pkg.SearchResult{}, errors.New("boom"))` → `fetchAllPRs` returns `"github_error"` → `Poll` returns early at the `if abortReason != ""` branch, before `processPRs` / `SaveCursor`) does NOT call `Scan`. Assert `Poll` returns nil, `fakeMetrics.IncPollCycleArgsForCall(0) == "github_error"` (proves the abort path was taken), and `ScanCallCount() == 0`. There is no reachable `rate_limited` abort in `fetchAllPRs` — do not add one.
   - the success case asserts `ScanCallCount() == 1` and `IncPollCycleArgsForCall(0) == "success"`.
   - build both watchers with `pkg.NewWatcher(...)` directly (as the "never fails the poll cycle when the scan aborts" test in `pkg/unreviewed_scanner_test.go` does), passing `new(mocks.UnreviewedScanner)`, with a temp-dir cursor path; `newTestWatcher*` hard-code `newDisabledUnreviewedScanner()` — do not change their signatures.
   These tests must fail if the `Scan` call is deleted from `Poll` or moved before the abort check.

4. **Skip, don't queue, overlapping scans.** Replace `s.mu.Lock()` at the top of `Scan` with `if !s.mu.TryLock() { glog.V(2).Infof("unreviewed scan already running, skipping"); return }` (plus the deferred unlock). Update the concurrency test: two concurrent `Scan` calls with one unreviewed PR ⇒ `PublishCreateCallCount()` is exactly 1 AND `SetUnreviewedPRsCallCount()` is exactly 1 (the second call skipped). Keep the test deterministic: keep the `entered` channel, but make the first `PublishCreate` stub block until the second `Scan` goroutine has RETURNED (e.g. `secondDone := make(chan struct{})`; second goroutine runs `defer wg.Done(); s.Scan(ctx); close(secondDone)`; stub waits `select { case <-secondDone: case <-time.After(5 * time.Second): }`), so the second call provably hits `TryLock` while the first holds the mutex. The current 200 ms timeout is not enough — a second goroutine scheduled after it acquires the lock, runs a full scan, and makes `SetUnreviewedPRsCallCount()` 2. Rename the test to "skips an overlapping scan instead of queueing it". Update the `mu` field comment ("mu serialises Scan…") to say an overlapping Scan is skipped, not queued.

5. **Cleanups.**
   - `pkg/metrics.go`: replace the comment inside `(m *prometheusMetrics) SetUnreviewedPRs` that begins "A late-finishing older scan can overwrite a newer result here" — scans are serialised by the scanner's mutex, so a late older scan cannot overwrite a newer one; the metrics mutex is defence in depth against concurrent scrapes.
   - Guard the `glog.V(2)` "unreviewed scan found …" line with `len(unreviewed) > 0`.
   - In `ParseUnreviewedConfig`, validate `rawThreshold` only when at least one login parsed (same as `retryMax`); with the feature off, an empty or invalid threshold is ignored. When no logins parsed, return `UnreviewedConfig{RetryMax: retryMax}` without parsing the threshold (Threshold zero). Drop the `cfg.Threshold == 30m` assertion from the existing "yields no logins for a blank login list" test. Add tests: (`" , "`, `"0s"`, `-1`) ⇒ no error; (`" , "`, `"abc"`, `3`) ⇒ no error; (`"bot"`, `"0s"`, `3`) ⇒ error (existing "rejects invalid thresholds" table stays). Update the `UNREVIEWED_THRESHOLD` row in README.md and the `usage` tag of `UnreviewedThreshold` in `main.go` to add "Ignored (and not validated) when `REVIEWER_BOT_LOGINS` is empty", matching the `UNREVIEWED_RETRY_MAX` row.
   - Replace any `errors.Wrapf(ctx, err, "<constant string>")` with no format arguments in the scanner with `errors.Wrap(ctx, err, "<constant string>")`.
   - Add a `## Unreleased` bullet to `CHANGELOG.md` starting `fix:` summarising items 1, 2 and 4, plus that `UNREVIEWED_THRESHOLD` is now ignored (not validated) when the feature is off.

6. Before finishing, re-run the `<verification>` command and confirm it passes; then walk each requirement above against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Behaviour with `REVIEWER_BOT_LOGINS` empty stays unchanged: no GitHub calls, no publishes, no `github_pr_watcher_unreviewed_prs` series.
- The retry semantics from the previous prompt are unchanged apart from item 1 (trust error now aborts the cycle): same `DeriveTaskID`, same publisher, same budget and pruning rules.
- No `time.Now()` in business logic; errors via `github.com/bborbe/errors`; Info logs `glog.V(n)`-gated except the existing per-retry event line.
- Do not change `ReviewIgnoreExclusion`, `processPR`, cursor handling, or `processLabeledPRs`.
- Do not run `go mod vendor`.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.
</verification>
