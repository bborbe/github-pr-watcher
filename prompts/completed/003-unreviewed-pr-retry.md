---
status: completed
summary: Added a bounded unreviewed-PR retry pass to UnreviewedScanner that re-publishes the original review task via TaskPublisher.PublishCreate/DeriveTaskID up to UNREVIEWED_RETRY_MAX times per (PR, head SHA), with a new retries_total counter, a retries_exhausted gauge label, config/docs/tests.
execution_id: github-pr-watcher-unreviewed-gauge-exec-003-unreviewed-pr-retry
dark-factory-version: v0.198.7
created: "2026-10-13T09:38:09Z"
queued: "2026-10-13T09:42:47Z"
started: "2026-10-13T09:56:41Z"
completed: "2026-10-13T10:07:31Z"
---

# Retry a lost review by re-publishing the same task, at most three times

<summary>
- When a pull request is still waiting for a reviewer-bot review past the configured waiting time, the watcher re-sends the same review request by itself instead of waiting for a human to notice
- It re-sends at most three times per pull request and commit, spaced one waiting-time apart, and then stops
- The re-sent request is identical to the original (same task identity and same task name), so a review that is merely slow is not duplicated — it collapses onto the task already there. Known gap: if the pull request title was edited after the original request, the re-sent request carries a different task name and can create a second review task; this is documented
- A new commit on the pull request starts a fresh budget of three retries, because it is a new review
- Each re-send is counted in a new metric, so retry activity shows up on dashboards (a re-send is not proof the review arrived)
- The unreviewed gauge gains a marker for pull requests whose retries are used up and whose last retry has had a full waiting time without a review — that is what the alert fires on
- With no reviewer identity configured, nothing is re-sent and behaviour is unchanged; with retries set to zero, nothing is re-sent and every unreviewed pull request is immediately marked as exhausted
</summary>

<objective>
Extend the unreviewed-PR scan from the previous prompt so it heals the common case by itself: re-publish a still-unreviewed PR's original review task, a bounded number of times, and expose which PRs are still unreviewed after the last retry so the alert fires only when retrying did not help. On 2026-10-12 two review commands were lost downstream of the watcher; a single manual re-publish of the same task IDs produced both reviews ~10 min later, so this is the proven remedy, automated and bounded.
</objective>

<context>
Read CLAUDE.md if present, and `docs/dod.md`.

This prompt builds on the previous prompt in this series (`1-unreviewed-pr-gauge`), which added the `UnreviewedScanner` (`Scan(ctx)`), `pkg.UnreviewedConfig`, `REVIEWER_BOT_LOGINS`, `UNREVIEWED_THRESHOLD`, `GitHubClient.SearchOpenPRs` / `ListReviews`, the `github_pr_watcher_unreviewed_prs{repo,pr}` gauge with its replace-the-whole-set setter, the last-success timestamp gauge, and their tests and README docs. Read that code first — it is the unit you extend.

Pattern references — read each before writing:
- `pkg/watcher.go` — `processPR`, the block after the `cursorState.HeadSHAs` dedup check: `taskIDStr := DeriveTaskID(...)` and `w.publisher.PublishCreate(ctx, pr, taskIDStr, details)`. The retry MUST publish through this same `TaskPublisher.PublishCreate` with the same task-ID derivation.
- `pkg/filename.go` — `computePRTitle` and its doc comment: the agent controller dedupes on the TITLE PATH (`checkTitlePathFree`), not on `task_identifier`; the title embeds the slugified `pr.Title`. Also the comments above `BuildCreateCommand` in `pkg/watcher.go` and above `publishCreateCommand` in `pkg/command/trigger_pr_review_executor.go`.
- `pkg/taskid.go` — `DeriveTaskID` (deterministic) vs `DeriveTaskIDForce` (nonce). Use `DeriveTaskID` only.
- `pkg/factory/factory.go` — `CreateWatcher` builds `publisher := pkg.NewTaskPublisher(...)`; the scanner must receive THAT instance.
- `pkg/metrics.go` — counter pattern, `init()` registration, `.Add(0)` pre-initialisation; `pkg/metrics_export_test.go` — the `PRPublishedTotalForTest` accessor pattern.
- `main.go` — `application` int fields with `default:"…"` tags.
- bborbe/time `libtime.NewCurrentDateTime()` + `SetNow` — an injectable clock tests can advance.
</context>

<requirements>
1. **Config.** Add `RetryMax int` to `pkg.UnreviewedConfig`. In `main.go` add `UnreviewedRetryMax int` to `application` with the neighbours' full tag set (`required:"false" arg:"unreviewed-retry-max" env:"UNREVIEWED_RETRY_MAX" usage:"…" default:"3"`) and pass it into the `UnreviewedConfig` built for `CreateWatcher`. Validate it inside prompt 1's `pkg.ParseUnreviewedConfig`, extended to `ParseUnreviewedConfig(ctx context.Context, rawLogins, rawThreshold string, retryMax int) (UnreviewedConfig, error)`: only when at least one login parsed, a negative value returns an error naming `UNREVIEWED_RETRY_MAX`; when the feature is disabled the value is ignored. Update prompt 1's `ParseUnreviewedConfig` callers and table test, and keep `Run`'s `//nolint:funlen` line count accurate. Add a `publisher TaskPublisher` parameter to `pkg.NewUnreviewedScanner`, directly after `ghClient GitHubClient` (update every `pkg.NewUnreviewedScanner` call, including prompt 1's scanner tests and the `newTestWatcher*` helpers; disabled scanners may pass `&mocks.TaskPublisher{}`), and inject the SAME `publisher` instance `CreateWatcher` already builds via `pkg.NewTaskPublisher(...)` — never construct a second publisher, or `TaskConfig` (stage, suffix, slug/title lengths, target vault) can drift and change the title path.

2. **Retry state.** The scanner keeps an in-memory map keyed by task ID (`DeriveTaskID(owner, repo, number, headSHA).String()`) → `{prKey string, attempts int, lastAttempt libtime.DateTime}`. It is only read or written while `Scan` holds the scanner's `sync.Mutex` from prompt 1 (`/check` runs `Poll` concurrently with the ticker; without the lock two scans race on the map — a fatal concurrent map write in Go — and can both publish the same retry). Not persisted: a pod restart can grant up to `RetryMax` more retries, which is acceptable. The key includes the head SHA, so a new commit gets a fresh budget. `prKey` (`owner/repo#n`, as `prKey` builds it) is stored because the task ID is a UUID5 and cannot be reversed. After a complete scan, prune only entries whose `prKey` is absent from this scan's full `SearchOpenPRs` result (PR closed or merged). Do NOT prune on any other skip — threshold skip after an `UpdatedAt` bump, filter toggle, trust error, empty head SHA, park, or a review appearing — or the budget resets and the at-most-`RetryMax`-per-(PR, head SHA) constraint breaks.

3. **Retry decision.** Only after a complete, successful scan (the same condition under which prompt 1 publishes the gauge) — on a partial, failed or cancelled scan no retry fires and the map is untouched — for each PR in the unreviewed set:
   - `attempts < RetryMax` AND (`attempts == 0` OR `now − lastAttempt ≥ Threshold`) ⇒ re-publish via `TaskPublisher.PublishCreate(ctx, pr, taskID, details)`, passing the unmodified `pr` and the `details` the scan fetched (no field rewrites — the title path depends on them). On `true`: increment `attempts`, set `lastAttempt = now`, call the retry-counter method, and log `glog.Infof("unreviewed retry pr=%s/%s#%d sha=%s task_id=%s attempt=%d/%d", …)` — one operator-facing line per retry, bounded by the budget, deliberately not `V`-gated. On `false`: change nothing — neither `attempts` nor `lastAttempt`, no counter — so it is eligible again on the next scan.
   - Otherwise ⇒ no publish this cycle.
   - Never use the force path; never publish anything other than the create command; never mutate GitHub.
   - On `ctx.Done()` during the retry loop: stop retrying (remaining PRs wait for the next scan), but still publish the gauge for the scan that completed.

4. **Metrics.**
   - New counter `github_pr_watcher_unreviewed_retries_total{repo}` (`owner/repo`), incremented via a new `Metrics` method `IncUnreviewedRetry(repo string)`. Register in `init()`; regenerate `mocks/metrics.go`; expose it to tests via `UnreviewedRetriesTotalForTest()` in `pkg/metrics_export_test.go` (same pattern as `PRPublishedTotalForTest`).
   - Add a third label `retries_exhausted` (`"true"` / `"false"`) to the `github_pr_watcher_unreviewed_prs` gauge. `"true"` iff `RetryMax == 0`, OR (`attempts ≥ RetryMax` AND `now − lastAttempt ≥ Threshold`) — the last retry has had a full waiting time and still no review arrived. Evaluate it after this cycle's retry decisions.
   - Carry it as a `RetriesExhausted bool` field on prompt 1's `UnreviewedPR`; `SetUnreviewedPRs` writes `strconv.FormatBool(pr.RetriesExhausted)` as the third label value. Keep the replace-the-whole-set semantics of `SetUnreviewedPRs`: regenerate the mock, and update every caller and every test prompt 1 added — including its real-`prometheusMetrics` read-back test, whose 2-label expectations no longer match a 3-label vec — plus the README `## Metrics` docs prompt 1 wrote.

5. **Scanner doc comment.** This prompt deliberately supersedes prompt 1's constraint "the scan must never publish tasks": update the `UnreviewedScanner` doc comment to say it re-publishes create commands via `TaskPublisher.PublishCreate` and still never mutates GitHub.

6. **Tests (Ginkgo/Gomega, counterfeiter fakes, clock `libtime.NewCurrentDateTime()` advanced with `SetNow`).** Reuse prompt 1's fixtures (real filter chain, `HeadSHA: ""` on search results). Cover at least:
   - first sighting past threshold ⇒ exactly one `PublishCreate` call whose task ID equals `pkg.DeriveTaskID(owner, repo, n, sha).String()`, `IncUnreviewedRetry` called once with `owner/repo`, gauge pair reported with `retries_exhausted="false"`
   - next scan with the clock advanced by `Threshold − 1s` ⇒ no publish; advanced to exactly `Threshold` ⇒ publishes (pins `≥`)
   - advance one threshold at a time ⇒ publishes 2 and 3; a scan right after publish 3 (no clock advance) still reports `retries_exhausted="false"`; after one more threshold the 4th scan publishes nothing and reports `retries_exhausted="true"`
   - every retry's task ID equals the explicit `pkg.DeriveTaskID(...)` value (never a force/nonce ID)
   - `PublishCreate` returns `false` ⇒ `IncUnreviewedRetry` not called; the next scan WITHOUT advancing the clock calls `PublishCreate` again; afterwards the PR still gets the full 3 successful re-publishes
   - exhaust the budget on SHA A, then return SHA B (`UpdatedAt` past threshold) ⇒ publishes with `DeriveTaskID(…, B).String()`, which differs from A's ID
   - exhaust the budget (3 publishes), then one complete scan with the PR's `UpdatedAt` within `Threshold` (skipped), then advance the clock ≥ `Threshold` past the 3rd publish and scan with `UpdatedAt` past threshold again ⇒ no 4th publish and `retries_exhausted="true"` (a skip does not reset the budget); separately, after one retry the PR is absent from `SearchOpenPRs` for one complete scan, then reappears unreviewed ⇒ publishes immediately with no clock advance (closed-PR pruning)
   - two goroutines call `Scan` on one scanner with one unreviewed PR; the first `PublishCreateStub` call signals `entered`, then waits up to 200ms for a second `PublishCreateStub` call (reachable only if the lock is missing) before returning `true`; the second goroutine starts after `entered` ⇒ `PublishCreateCallCount()` is exactly 1 after both return (`make test` runs `-race=false` unless `ENABLE_RACE` is set, so the count assertion is the guard)
   - `ParseUnreviewedConfig`: (`" , "`, `"30m"`, `-1`) ⇒ no error, no logins; (`"bot"`, `"30m"`, `-1`) ⇒ error; (`"bot"`, `"30m"`, `0`) ⇒ `RetryMax` 0
   - trust-check error on a PR that had attempts=1 (a `trust.Func` that succeeds, errors for one scan, then succeeds) ⇒ entry not pruned: the next normal scan with the clock < `Threshold` after retry 1 makes no `PublishCreate` call; advanced to `Threshold` ⇒ publishes retry 2
   - a scan whose second PR's `ListReviews` errors ⇒ no `PublishCreate` call at all this cycle
   - two unreviewed PRs, ctx cancelled inside the first `PublishCreateStub` (which returns `true`) ⇒ `PublishCreateCallCount()` is 1 and `SetUnreviewedPRs` is still called once with both PRs
   - `RetryMax = 0` ⇒ never publishes, gauge reports `retries_exhausted="true"`
   - `REVIEWER_BOT_LOGINS` empty ⇒ no publish, no GitHub calls
   - real `prometheusMetrics`: read `github_pr_watcher_unreviewed_retries_total{repo}` via the test accessor before and after and assert a delta of +1 (the registry is global); after the increment, `testutil.GatherAndCount(prometheus.DefaultGatherer, "github_pr_watcher_unreviewed_retries_total")` is ≥ 1 (proves `init()` registration — the accessor alone bypasses the registry); assert the gauge with `testutil.GatherAndCompare(prometheus.DefaultGatherer, strings.NewReader(expected), "github_pr_watcher_unreviewed_prs")` (as prompt 1 does) against expected exposition text showing the PR with `retries_exhausted="true"` and NO `"false"` series after the flip — never probe absence with `WithLabelValues`, which creates the series

7. **Docs.** In the README (`## Environment Variables` and `## Metrics`): `UNREVIEWED_RETRY_MAX`, the retry behaviour, the new counter, the `retries_exhausted` label. State that:
   - a retry cannot revive a task that already exists but never dispatched — only a new commit does — so an exhausted PR needs a human;
   - the controller dedups on the task title path, so editing a PR's title between the original publish and a retry can produce a second review task for the same commit;
   - each retry also increments `github_pr_watcher_prs_total{command="create"}` and may cost 1–2 extra GitHub calls (`PublishCreate`'s review-ignore lookup);
   - on a cold start a PR can be published by the poll and retried by the scan in the same cycle — harmless (dedup), but it counts as a retry;
   - if `PublishCreate` keeps returning `false` (e.g. Kafka down), attempts never reach the budget, so `retries_exhausted` never flips — watch `github_pr_watcher_prs_total{command="error"}` for that case.
   Add a `## Unreleased` bullet to `CHANGELOG.md` starting `feat:`.

8. Before finishing, re-run the `<verification>` command and confirm it passes; then walk each requirement above and each test case in 6 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Retries publish ONLY through `TaskPublisher.PublishCreate` with `DeriveTaskID` — never `DeriveTaskIDForce`, never an override, never a label or any other GitHub mutation.
- At most `RetryMax` successful re-publishes per (PR, head SHA) per process lifetime.
- With `REVIEWER_BOT_LOGINS` empty, runtime behaviour is identical to before this prompt series and `UNREVIEWED_RETRY_MAX` is not range-validated (a non-integer still fails the argument parser; only an explicitly malformed `UNREVIEWED_THRESHOLD` can fail startup, as prompt 1 already defines).
- The normal poll path (`processPR`, cursor `HeadSHAs`, `processLabeledPRs`) is not modified; the retry lives in the scanner.
- No `time.Now()` in business logic — use the injected `libtime.CurrentDateTimeGetter`.
- Errors via `github.com/bborbe/errors`; Info logs `glog.V(n)`-gated except the single per-retry event line in requirement 3.
- Do not run `go mod vendor`.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.
</verification>
