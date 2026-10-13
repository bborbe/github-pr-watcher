---
status: completed
summary: Added a github_pr_watcher_unreviewed_prs gauge plus a last-successful-scan timestamp, driven by a new read-only UnreviewedScanner that runs once per poll over every open publishable PR, with REVIEWER_BOT_LOGINS/UNREVIEWED_THRESHOLD config, GitHub client SearchOpenPRs/ListReviews, tests, and docs
execution_id: github-pr-watcher-unreviewed-gauge-exec-002-unreviewed-pr-gauge
dark-factory-version: v0.198.7
created: "2026-10-13T09:22:44Z"
queued: "2026-10-13T09:38:08Z"
started: "2026-10-13T09:38:10Z"
completed: "2026-10-13T09:56:39Z"
---

# Expose a gauge of open PRs still waiting for a reviewer-bot review

<summary>
- The watcher reports, on every poll cycle, which open pull requests it would hand to the reviewer but that still have no review from the reviewer bot on their latest commit after a configurable waiting time
- Each such pull request appears as its own series on a new Prometheus gauge, so an alert can name the exact repository and PR number
- A pull request leaves the gauge as soon as the reviewer bot reviews its current head commit, or when it is closed, drafted, or otherwise stops qualifying
- Only pull requests the watcher would actually hand to the reviewer are counted: the same filters, the same trusted-author check, and no override-labelled, oversized-parked or head-less pull requests
- The scan looks at all open pull requests in scope, not just the ones updated since the last poll, so a pull request whose review was lost long ago is still reported
- The reviewer bot identities and the waiting time are configured per deployment; with no reviewer identity configured the feature is off and nothing changes
- A failed or interrupted scan leaves the previous gauge values in place and never fails the poll cycle; a separate timestamp shows when the last scan succeeded, so a scan that keeps failing is visible too
</summary>

<objective>
Add a `github_pr_watcher_unreviewed_prs` gauge that marks every open, publishable, trusted-author PR whose head SHA has no review from a configured reviewer-bot login once it has waited longer than a configured threshold, plus a last-successful-scan timestamp. This is the metric behind an end-to-end "review never arrived" alert: on 2026-10-12 two PRs' review commands were lost downstream of the watcher and nothing noticed for ~20 h, because every existing signal (publish counters, poll cycles) looked healthy. A follow-up prompt adds an automatic retry on top of this scan.
</objective>

<context>
Read CLAUDE.md if present, and `docs/dod.md`.

Pattern references — read each before writing:
- `pkg/watcher.go` — `watcher.Poll` (the poll cycle) and `watcher.processLabeledPRs`: the existing precedent for a second pass that deliberately runs OUTSIDE the `updated:>=cursor` window. The new scan follows the same shape (fetch, ctx check per PR, filter, act) and is invoked from `Poll` after `processLabeledPRs`.
- `pkg/watcher.go` — `processPR`: how a `pkg.PullRequest` becomes a `filter.PR` (note `RepoKey: "github.com/" + pr.Owner + "/" + pr.Repo`) for `taskCreationFilter.Skip`, how `trust.Trust.IsTrusted(ctx, trust.PR{AuthorLogin: …})` is called, and its fail-closed branch on an empty `details.HeadSHA`. The scan must use the same `taskCreationFilter` and `trustDecision` instances the watcher holds.
- `pkg/watcher.go` — `oversized(...)` and `ReviewIgnoreExclusion(...)`: how `PublishCreate` decides to park an oversized trusted PR at human_review instead of handing it to the reviewer. `botLoginSuffix` (the `[bot]` constant).
- `pkg/githubclient.go` — `GitHubClient` interface; `SearchPRs` and `SearchLabeledPRs`, both built on the shared `searchIssuePRs`; `GetPRDetails` (returns `PRDetails.HeadSHA`); `ListPRFiles` (the pagination precedent: `ListOptions{PerPage: 100}`, ctx check per page, follow `resp.NextPage`). The search result's `PullRequest.HeadSHA` is EMPTY — the head SHA must come from `GetPRDetails`. Rate-limit capture is already done by `rateCapturingTransport`.
- `pkg/githubclient_test.go` — the httptest style (`buildClient`, `pkg.NewForTest`) used by the `SearchPRs` pagination cases.
- `pkg/metrics.go` — `Metrics` interface + counterfeiter directive, the `github_pr_watcher_` prefix, `init()` registration, `SetRateLimitRemaining` as the gauge-setter precedent. `pkg/metrics_executor_test.go` already uses `prometheus/testutil`.
- `pkg/handler/webhook_handler.go` (`clock libtime.CurrentDateTimeGetter`) and `pkg/command/trigger_pr_review_executor.go` (`currentDateTime libtime.CurrentDateTimeGetter`) — the precedent for an injected clock; tests build one with `libtime.CurrentDateTimeGetterFunc` (see `pkg/command/trigger_pr_review_executor_test.go`). Note: `pkg/filter/age_filter.go` deliberately uses NO clock, and the watcher currently holds none.
- `main.go` — `application` struct tags (`TRUSTED_AUTHORS`, `BOT_ALLOWLIST`, `MAX_PR_AGE`), `pkg.ParseBotAllowlist` for comma lists, `parseMaxPRAge` for duration parsing, `currentDateTime` in `Run`; `pkg/factory/factory.go` `CreateWatcher` for wiring.
- `pkg/watcher_test.go` (`newTestWatcher` and its sibling helpers) and `pkg/pkg_suite_test.go` for the Ginkgo/Gomega + counterfeiter test style.
- go-github v62 `PullRequestsService.ListReviews` and `PullRequestReview.{User,CommitID}` — available via the existing client, currently unused.
</context>

<requirements>
1. **Config.** In `pkg` add `type UnreviewedConfig struct { Logins []string; Threshold libtime.Duration }` (doc-commented). In `main.go` add two `application` fields in the existing tag style:
   - `ReviewerBotLogins` — env `REVIEWER_BOT_LOGINS`, comma-separated GitHub logins (e.g. `octopus-pr-reviewer,octopus-pr-reviewer-dev`), default empty. Parse like `pkg.ParseBotAllowlist` (trim, drop empty entries). No logins after parsing ⇒ feature disabled.
   - `UnreviewedThreshold` — env `UNREVIEWED_THRESHOLD`, duration, default `30m`. Parse with a dedicated helper (do not reuse `parseMaxPRAge`, whose error text names `MAX_PR_AGE`); reject non-positive values with a wrapped startup error.
   Put both parses in one exported helper in `pkg`, `ParseUnreviewedConfig(ctx context.Context, rawLogins, rawThreshold string) (UnreviewedConfig, error)` (logins via `ParseBotAllowlist`; threshold via `libtime.ParseDuration`, error text naming `UNREVIEWED_THRESHOLD`), so `Run` makes one call and the parsing is testable from `pkg_test` (`main_test.go` is an external package with only a compile check), and keep the `//nolint:funlen` comment on `Run` accurate (update its line count, or remove the directive if `Run` is back under the cap). Pass one `pkg.UnreviewedConfig` plus the existing `currentDateTime` from `Run` into `pkg/factory/factory.go` `CreateWatcher` as new parameters. `CreateWatcher` (new trailing params `unreviewedConfig pkg.UnreviewedConfig, currentDateTime libtime.CurrentDateTimeGetter`) constructs the scanner (req 5) via `pkg.NewUnreviewedScanner(ghClient GitHubClient, metrics Metrics, currentDateTime libtime.CurrentDateTimeGetter, scope string, taskCreationFilter filter.TaskCreationFilter, trustDecision trust.Trust, overrideLabel string, maxAdditions, maxChangedFiles int, cfg UnreviewedConfig) UnreviewedScanner` from the `ghClient`, `scope`, `taskCreationFilter`, `trustDecision`, `metrics`, override label, `maxAdditions` and `maxChangedFiles` it already receives, and passes it to `pkg.NewWatcher` as a new trailing `unreviewedScanner UnreviewedScanner` parameter. Update every `pkg.NewWatcher` call in tests (the `newTestWatcher*` helpers and the inline calls in `pkg/watcher_test.go`) to pass a disabled scanner (no logins), so existing assertions such as `Expect(ghClient.SearchPRsCallCount()).To(Equal(1))` pass unchanged — do not edit them.

2. **Login matching.** A review counts when its author login — lower-cased first, then with any trailing `[bot]` suffix stripped (reuse `botLoginSuffix`) — equals, exactly, one of the configured logins normalised the same way. An empty login never matches. Put this in a small pure function with a table test.

3. **GitHub client.**
   - `SearchOpenPRs(ctx context.Context, scope string, page int) (SearchResult, error)` — query `is:pr is:open archived:false user:<scope>` with NO `updated:` qualifier, implemented like `SearchLabeledPRs` via `searchIssuePRs`. Do NOT derive a `since` from `MAX_PR_AGE`: an empty `MAX_PR_AGE` means 0 (disabled), so a `now − MAX_PR_AGE` window would return nothing, and age parity already comes from `taskCreationFilter`, which contains the age filter.
   - `ListReviews(ctx context.Context, owner, repo string, number int) ([]Review, error)` with `type Review struct { AuthorLogin, CommitID string }`. Implement with `c.client.PullRequests.ListReviews(ctx, owner, repo, number, opts)`, paginating exactly like `ListPRFiles`. Map `r.GetUser().GetLogin()` and `r.GetCommitID()` nil-safely (a deleted-account reviewer yields login `""`). Wrap errors with `errors.Wrapf(ctx, err, "list reviews %s/%s#%d", owner, repo, number)`; add no rate-limit code.
   - httptest cases in `pkg/githubclient_test.go`: `SearchOpenPRs` sends a `q` that contains `is:open` and `user:<scope>` and no `updated:`, and a `Link: <…?page=2>; rel="next"` header yields `HasNextPage=true, NextPage=2`; `ListReviews` maps a review with `"user": null` to login `""`; `ListReviews` page 1 returns one review plus a `rel="next"` Link, page 2 a second review — both returned with correct login/CommitID and the server saw 2 requests; an HTTP 500 returns a non-nil error.
   - Regenerate the counterfeiter mock.

4. **Metrics.** In `pkg/metrics.go`:
   - `GaugeVec` `github_pr_watcher_unreviewed_prs`, labels `repo` (`owner/repo`) and `pr` (number as a string), value `1` per unreviewed PR.
   - `Gauge` `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds` — Unix seconds of the last complete scan; zero until the first one.
   - `type UnreviewedPR struct { Repo string; Number int }` (`Repo` = `owner/repo`) and one `Metrics` method `SetUnreviewedPRs(prs []UnreviewedPR, scannedAt libtime.DateTime)` that, holding a `sync.Mutex` field of `prometheusMetrics` (`/check` can run `Poll` concurrently with the ticker), calls `Reset()`, then `WithLabelValues(pr.Repo, strconv.Itoa(pr.Number)).Set(1)` per entry, then sets the timestamp gauge to `float64(scannedAt.Unix())`. The scanner passes its injected clock's `Now()`; `NewMetrics()` stays parameterless. (A late-finishing older `/check` scan can overwrite a newer result — note it in a code comment; no code change.)
   - Register both in `init()`; regenerate the mock.

5. **Scan.** Its own unit in a new file (e.g. `pkg/unreviewed_scanner.go`): `type UnreviewedScanner interface { Scan(ctx context.Context) }` — no error return, so "never fails Poll" holds by type — plus a constructor and counterfeiter directive. `Poll` calls `Scan` once, after `SaveCursor` and before `IncPollCycle("success")`, so a slow scan never delays cursor persistence. When `Poll` returns early on a main-search abort, the scan is skipped for that cycle — that is intended; do not change it. The scanner holds a `sync.Mutex` field that `Scan` locks for its whole run (`/check` runs `Poll` through `libhttp.NewBackgroundRunHandler`, whose `ParallelSkipper` only de-duplicates `/check` calls, not `/check` vs the ticker), so two scans never interleave. Split per-PR qualification into a helper (e.g. `qualify(ctx, pr) (PRDetails, bool, error)`) so `Scan` stays under the `.golangci.yml` gocognit/nestif/funlen caps. Behaviour:
   - Disabled (no logins) ⇒ return immediately: no GitHub calls, no metric call.
   - Fetch all open PRs with `SearchOpenPRs`, paginating like `fetchAllPRs` EXCEPT its cancel branch: `fetchAllPRs` returns `(nil, "")` on `ctx.Done()`, which is indistinguishable from zero PRs. In the scan, `ctx.Done()` before or between pages ⇒ return WITHOUT calling the metric method.
   - For each PR, in order — any skip means "not counted, not an error":
     - on `ctx.Done()` return immediately WITHOUT calling the metric method (a cancelled scan is incomplete);
     - skip if `taskCreationFilter.Skip(...)` (build `filter.PR` exactly as `processPR` does);
     - skip if the watcher's override label is non-empty and the PR carries it (such a PR gets a `pr-override` task via `PublishOverride`, never a bot review);
     - skip if not trusted; a trust error ⇒ `glog.Warningf` and skip that PR;
     - skip if `now − pr.UpdatedAt < Threshold` — `UpdatedAt` is the reviewable-since clock: it moves on open, new pushes and draft→ready, which should restart the wait;
     - `GetPRDetails`; skip if `details.HeadSHA == ""` (mirrors `processPR`'s fail-closed branch);
     - skip a PR `PublishCreate` would park: if `oversized(details, Exclusion{}, maxAdditions, maxChangedFiles)` is true, compute `ReviewIgnoreExclusion(ctx, ghClient, pr.Owner, pr.Repo, pr.Number, pr.HTMLURL)` and skip when `oversized(details, exclusion, maxAdditions, maxChangedFiles)` is still true (the extra calls happen only for raw-oversized PRs);
     - `ListReviews`; count the PR as unreviewed iff NO review by a matching login has `CommitID == details.HeadSHA`. A review in any state (APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED) counts; a bot review on an older SHA does NOT.
   - Any search, `GetPRDetails` or `ListReviews` error ⇒ `glog.Warningf` and return WITHOUT calling the metric method (no partial set; previous values and timestamp stay). `ReviewIgnoreExclusion` returns no error — on a `.reviewignore` read failure it logs and returns the zero `Exclusion` (park-more direction), so the scan, exactly like `PublishCreate`, treats that PR as parked and skips it. Do NOT change `ReviewIgnoreExclusion`'s signature.
   - On success: publish the full set (possibly empty) via the new `Metrics` method; log count + `owner/repo#n` list at `glog.V(2)`.

6. **Tests (Ginkgo/Gomega, counterfeiter fakes, clock via `libtime.CurrentDateTimeGetterFunc`).** Scanner tests use the REAL filter chain (`filter.TaskCreationFilters{filter.NewDraftFilter(), filter.NewBotAuthorFilter(...), filter.NewRepoAllowlistFilter(...)}`) and `trust.NewAuthorAllowlist(...)`, not fakes (exception: the trust-error case uses a `trust.Func` returning an error, since the real allowlist never errors), so `processPR` parity (incl. the `github.com/<owner>/<repo>` RepoKey) is exercised. Search-result fixtures MUST have `HeadSHA: ""` (as the real search returns); the head SHA comes only from the `GetPRDetails` fake. Every "counted" case asserts the EXACT argument passed to the metric method (e.g. one pair repo=`bborbe/repo`, pr=`42`), not just the call count. Cover at least:
   - disabled ⇒ `SearchOpenPRsCallCount`, `GetPRDetailsCallCount`, `ListReviewsCallCount` and the metric-method call count are all 0
   - PR updated `threshold − 1s` ago ⇒ not counted, no `GetPRDetails`/`ListReviews` call; PR updated exactly `threshold` ago ⇒ counted
   - PR past threshold with no reviews ⇒ counted with exact labels
   - bot review on the head SHA ⇒ not counted; bot review only on an OLDER SHA ⇒ counted
   - review by a non-bot login on the head SHA ⇒ counted
   - `octopus-pr-reviewer[bot]` review matches configured `octopus-pr-reviewer`
   - draft PR, repo outside a configured allowlist, untrusted author, trust error, PR carrying the override label ⇒ not counted, no `GetPRDetails` call
   - oversized PR that `PublishCreate` would park ⇒ not counted; raw-oversized PR whose `.reviewignore` exclusion (fake `GetReviewIgnore` returning a `mocks.ReviewIgnoreMatcher` + `ListPRFiles`) brings it under the limits ⇒ counted; under-limit PR ⇒ `GetReviewIgnoreCallCount` is 0; empty `HeadSHA` from `GetPRDetails` ⇒ not counted
   - two PRs past threshold where the second's `ListReviews` errors ⇒ metric method NOT called (no partial set); same for a `GetPRDetails` error, a search error on page 2, ctx cancelled before the first search page, and ctx cancelled mid-loop
   - Poll-level: a watcher wired with an ENABLED real scanner whose `ListReviews` errors ⇒ `Poll` returns nil, `IncPollCycleArgsForCall(0)` is `"success"`, metric method not called
   - success with zero qualifying PRs ⇒ metric method called once with an empty slice
   - login table test (req 2): `OCTOPUS-PR-REVIEWER[BOT]` matches; `octopus-pr-reviewer-dev` does NOT match configured `octopus-pr-reviewer`; empty login `""` never matches; exact match; `[bot]` on the review side only; `[bot]` on the config side only; mixed case; `[BOT]`; `ParseUnreviewedConfig(ctx, " , ", "30m")` yields no logins (disabled); thresholds `"0s"`, `"-1m"`, `"abc"` return an error; `"45m"` yields 45m
   - real `prometheusMetrics`: `testutil.GatherAndCount(prometheus.DefaultGatherer, "github_pr_watcher_unreviewed_prs")` (proves `init()` registration) is 2 after setting {A,B}, 1 after {B}, 0 after {}; `testutil.GatherAndCompare` with expected exposition text proves label names/values (`pr="42",repo="bborbe/repo"`) and value 1; `testutil.ToFloat64` on the timestamp gauge equals `scannedAt.Unix()` after a `SetUnreviewedPRs` call (expose the gauge through a `pkg/metrics_export_test.go` accessor like `PRPublishedTotalForTest`). Never read a removed series via `WithLabelValues` — it re-creates it.

7. **Docs.** Add `REVIEWER_BOT_LOGINS` and `UNREVIEWED_THRESHOLD` rows to the README `## Environment Variables` table, and a short `## Metrics` section describing both new metrics and their labels. State the known limitation: comments and label changes also move `UpdatedAt` (including the watcher's own trivial-PR label), which restarts the wait and can delay a report. State the per-poll API cost (≈2 core calls per qualifying open PR, more for raw-oversized PRs) on the shared token. Add a `## Unreleased` bullet to `CHANGELOG.md` starting `feat:`.

8. Before finishing, re-run the `<verification>` command and confirm it passes; then walk each requirement above and each test case in 6 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing behaviour is unchanged when `REVIEWER_BOT_LOGINS` is empty: no extra GitHub calls, same publish path, same counters, no `github_pr_watcher_unreviewed_prs` series exported. The only new exported series is `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds`, which stays `0` (alerts on it must guard `> 0`).
- The scan must never publish tasks, apply labels, or mutate anything on GitHub — it is read-only.
- No `time.Now()` in business logic — use the injected `libtime.CurrentDateTimeGetter`.
- Errors via `github.com/bborbe/errors` (`errors.Wrapf(ctx, err, …)`), no `fmt.Errorf`, no bare `return err`.
- Info logs must be `glog.V(n)`-gated.
- Do not run `go mod vendor`; `vendor/` is not committed.
- Keep the poll cycle's existing metrics, cursor handling and `processLabeledPRs` untouched apart from the single call into the new scan.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.
</verification>
