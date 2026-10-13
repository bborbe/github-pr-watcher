---
status: draft
created: "2026-10-13T09:22:44Z"
---

# Expose a gauge of open PRs still waiting for a reviewer-bot review

<summary>
- The watcher reports, on every poll cycle, which open pull requests it would hand to the reviewer but that still have no review from the reviewer bot on their latest commit after a configurable waiting time
- Each such pull request appears as its own series on a new Prometheus gauge, so an alert can name the exact repository and PR number
- A pull request leaves the gauge as soon as the reviewer bot reviews its current head commit, or when it is closed, drafted, or otherwise stops qualifying
- Only pull requests the watcher would actually publish are counted: the same draft, bot-author, WIP-title, age and repo-allowlist filters and the same trusted-author check apply
- The scan looks at all open pull requests in scope, not just the ones updated since the last poll, so a pull request whose review was lost long ago is still reported
- The reviewer bot identities and the waiting time are configured per deployment; with no reviewer identity configured the feature is off and the gauge stays empty
- A failed scan is logged and leaves the previous gauge values in place; it never fails the poll cycle
</summary>

<objective>
Add a `github_pr_watcher_unreviewed_prs` gauge that marks every open, publishable, trusted-author PR whose head SHA has no review from a configured reviewer-bot login once it has waited longer than a configured threshold. This is the metric behind an end-to-end "review never arrived" alert: on 2026-10-12 two PRs' review commands were lost downstream of the watcher and nothing noticed for ~20 h, because every existing signal (publish counters, poll cycles) looked healthy.
</objective>

<context>
Read CLAUDE.md if present, and `docs/dod.md`.

Pattern references — read each before writing:
- `pkg/watcher.go` — `watcher.Poll` (the poll cycle) and `watcher.processLabeledPRs`: the existing precedent for a second pass that deliberately runs OUTSIDE the `updated:>=cursor` window. The new scan follows the same shape (fetch, ctx.Done() check per PR, filter, act) and is invoked from `Poll` after `processLabeledPRs`.
- `pkg/watcher.go` — `processPR`: shows how a `pkg.PullRequest` is turned into a `filter.PR` (note `RepoKey: "github.com/" + pr.Owner + "/" + pr.Repo`) for `taskCreationFilter.Skip`, and how `trust.Trust.IsTrusted(ctx, trust.PR{AuthorLogin: …})` is called. The scan must use the same `taskCreationFilter` and `trustDecision` instances the watcher already holds, so it counts exactly what the watcher publishes.
- `pkg/githubclient.go` — `GitHubClient` interface, `SearchPRs` (query `is:pr is:open archived:false user:<scope> updated:>=<since>`), `GetPRDetails` (returns `PRDetails.HeadSHA`). The search result's `PullRequest.HeadSHA` is EMPTY — the head SHA must come from `GetPRDetails`.
- `pkg/metrics.go` — `Metrics` interface + counterfeiter directive, the `github_pr_watcher_` prefix, `init()` registration, and `SetRateLimitRemaining` as the gauge-setter precedent.
- `pkg/filter/age_filter.go` — how "now" is obtained via an injected `libtime.CurrentDateTimeGetter` (never `time.Now()`), and how `MAX_PR_AGE` bounds PR age.
- `main.go` — the `application` struct env/arg tags (`TRUSTED_AUTHORS`, `MAX_PR_AGE`, `POLL_INTERVAL`) and `parseMaxPRAge` for duration parsing; `pkg/factory/factory.go` `CreateWatcher` for wiring.
- `pkg/watcher_test.go` (`newTestWatcher`) and `pkg/pkg_suite_test.go` for the Ginkgo/Gomega + counterfeiter test style.
- go-github v62 `PullRequestsService.ListReviews` — available via the existing client, currently unused.
</context>

<requirements>
1. **Config.** In `main.go` add two fields to `application`, following the existing tag style:
   - `ReviewerBotLogins` — env `REVIEWER_BOT_LOGINS`, comma-separated GitHub logins (e.g. `octopus-pr-reviewer,octopus-pr-reviewer-dev`). Default empty. Empty ⇒ the scan is disabled (no-op) and the gauge exports no series.
   - `UnreviewedThreshold` — env `UNREVIEWED_THRESHOLD`, a duration, default `30m`. Parse like `parseMaxPRAge`; reject non-positive values with a wrapped error at startup.
   Thread both through `pkg/factory/factory.go` `CreateWatcher` into the watcher.

2. **Login matching.** A review counts when its author login, with any trailing `[bot]` suffix stripped and compared case-insensitively, equals one of `ReviewerBotLogins` (also stripped). GitHub App reviews arrive as `octopus-pr-reviewer[bot]` while operators write `octopus-pr-reviewer` — both must match. Put this in a small pure function with a table test.

3. **GitHub client.** Add `ListReviews(ctx, owner, repo string, number int) ([]Review, error)` to the `GitHubClient` interface, where `Review` carries at least the author login and the commit SHA the review was submitted on (`CommitID`). Paginate until exhausted. Follow `GetPRDetails` for error wrapping and rate-limit handling. Regenerate the counterfeiter mock.

4. **Metric.** In `pkg/metrics.go` add a `GaugeVec` named `github_pr_watcher_unreviewed_prs` with labels `repo` (`owner/repo`) and `pr` (the number as a string). Value `1` per unreviewed PR. Add one `Metrics` method that replaces the whole set atomically from a slice of `(repo, pr)` pairs (reset, then set), so a PR that stops qualifying disappears on the next successful scan. Register it in `init()`. Regenerate the mock.

5. **Scan.** Implement the pass as its own unit (new file, e.g. `pkg/unreviewed_scanner.go`, with an interface + constructor + counterfeiter directive) invoked once per `Poll`, after `processLabeledPRs`:
   - Disabled (no logins) ⇒ return immediately, touch nothing.
   - Fetch all open PRs in scope with `SearchPRs`, using `since = now − MAX_PR_AGE` (the age filter would drop anything older anyway), paginating like `fetchAllPRs`.
   - For each PR: honour `ctx.Done()`; skip if `taskCreationFilter.Skip(...)`; skip if not trusted (a trust error ⇒ log and skip that PR); skip if the PR carries the watcher's `overrideLabel` (non-empty) — such a PR gets a `pr-override` task via `PublishOverride` instead of a review, so it never receives a bot review and must not be reported; skip if `now − pr.UpdatedAt < UnreviewedThreshold`. `UpdatedAt` is the reviewable-since clock on purpose: it moves on open, on new pushes and on draft→ready, which are exactly the events that should restart the wait; a bot review also moves it, but such a PR is excluded below anyway.
   - For a PR past the threshold: get the head SHA via `GetPRDetails`, list reviews, and count the PR as unreviewed iff NO review by a matching login has `CommitID == headSHA`. A bot review on an older SHA does NOT count.
   - On any search or per-PR API error that prevents a complete result: log with `glog.Warningf` and leave the gauge untouched for this cycle (do not publish a partial set). Never return an error that fails the poll cycle.
   - On success: publish the full set via the new `Metrics` method (empty slice ⇒ gauge cleared).
   - Log the result at `glog.V(2)` (count + the `owner/repo#n` list).

6. **Tests (Ginkgo/Gomega, counterfeiter fakes, injected clock).** Cover at least:
   - disabled ⇒ no GitHub calls, no metric call
   - PR younger than threshold ⇒ not counted, no reviews call
   - PR past threshold with no reviews ⇒ counted
   - bot review on the head SHA ⇒ not counted; bot review only on an OLDER SHA ⇒ counted
   - review by a non-bot login on the head SHA ⇒ counted
   - `octopus-pr-reviewer[bot]` matches configured `octopus-pr-reviewer`
   - filtered PR (e.g. draft), untrusted author, and PR carrying the override label ⇒ not counted
   - `ListReviews` error ⇒ metric method NOT called (previous values kept), Poll still succeeds
   - success with zero qualifying PRs ⇒ metric method called with an empty set
   - one test that drives the real `prometheusMetrics` implementation and reads the gauge back (e.g. `testutil.ToFloat64` / `CollectAndCount` from `prometheus/client_golang/prometheus/testutil`) to prove a removed PR's series disappears after the next set — the label/reset boundary, not just the fake call.

7. **Docs.** Document `REVIEWER_BOT_LOGINS` and `UNREVIEWED_THRESHOLD` and the new metric wherever the README / docs list env vars and metrics (grep for `TRUSTED_AUTHORS` and `rate_limit_remaining` to find every place). Add a `## Unreleased` bullet to `CHANGELOG.md` starting `feat:`.

8. Before finishing, re-run the `<verification>` command and confirm it passes; then walk each requirement above and each test case in 6 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing behaviour is unchanged when `REVIEWER_BOT_LOGINS` is empty: no extra GitHub calls, same publish path, same counters.
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
