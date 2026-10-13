---
status: draft
---

# Retry a lost review by re-publishing the same task, at most three times

<summary>
- When a pull request is still waiting for a reviewer-bot review past the configured waiting time, the watcher re-sends the same review request by itself instead of waiting for a human to notice
- It re-sends at most three times per pull request and commit, spaced one waiting-time apart, and then stops
- The re-sent request uses the exact same task identity as the original, so a review that is merely slow is not duplicated — it collapses onto the task already running
- A new commit on the pull request starts a fresh budget of three retries, because it is a new review
- Each re-send is counted in a new metric, so healing shows up on dashboards
- The existing unreviewed gauge gains a marker for pull requests whose retries are used up, which is what the alert will fire on
- With no reviewer identity configured, or with retries set to zero, nothing is re-sent and behaviour is unchanged
</summary>

<objective>
Extend the unreviewed-PR scan (added by the previous prompt) so it heals the common case by itself: re-publish the PR's original `CreateTaskCommand` — same `DeriveTaskID(owner, repo, number, headSHA)`, `force=false` — up to `UNREVIEWED_RETRY_MAX` times per (PR, head SHA), one `UNREVIEWED_THRESHOLD` apart, and expose which PRs have exhausted their retries so the alert fires only when retrying did not help. On 2026-10-12 two review commands were lost downstream; a single manual re-publish of the same task IDs produced both reviews ~10 min later, so this is the proven remedy, automated and bounded.
</objective>

<context>
Read CLAUDE.md if present, and `docs/dod.md`.

This prompt builds on the previous prompt in this series (`1-unreviewed-pr-gauge`), which added `pkg/unreviewed_scanner.go` (or similar), `REVIEWER_BOT_LOGINS`, `UNREVIEWED_THRESHOLD`, `GitHubClient.ListReviews`, and the `github_pr_watcher_unreviewed_prs{repo,pr}` gauge. Read that code first — it is the unit you extend.

Pattern references — read each before writing:
- `pkg/watcher.go` — `processPR`, the block after the `cursorState.HeadSHAs` dedup check: `taskIDStr := DeriveTaskID(...)` and `w.publisher.PublishCreate(ctx, pr, taskIDStr, details)`. The retry MUST publish through this same `TaskPublisher.PublishCreate` with the same task-ID derivation, so the retry is byte-identical to the original publish (trust check, payload, routing all included).
- `pkg/taskid.go` — `DeriveTaskID` (deterministic) vs `DeriveTaskIDForce` (nonce). Use `DeriveTaskID` only. Never the force variant: a new task ID per retry would create duplicate review tasks.
- `pkg/command/trigger_pr_review_executor.go` — `publishCreateCommand`, the manual trigger path that today's successful manual re-publish went through; shows the metric labels used on publish (`create`, `kafka_error`).
- `pkg/metrics.go` — counter + gauge patterns, `init()` registration, label pre-initialisation with `.Add(0)`.
- `main.go` — env/arg tag style and duration parsing as used for `UNREVIEWED_THRESHOLD`.
</context>

<requirements>
1. **Config.** Add `UnreviewedRetryMax` — env `UNREVIEWED_RETRY_MAX`, integer, default `3`. `0` disables retries (the gauge from the previous prompt still works). Negative ⇒ wrapped startup error. Thread it to the scanner via `pkg/factory/factory.go` like the existing fields.

2. **Retry state.** The scanner keeps an in-memory map keyed by task ID (`DeriveTaskID(owner, repo, number, headSHA).String()`) → `{attempts int, lastAttempt libtime.DateTime}`. It is deliberately not persisted: a pod restart can grant up to `UnreviewedRetryMax` more retries, which is acceptable. Because the key includes the head SHA, a new commit naturally gets a fresh budget. Prune entries whose PR no longer appears in the unreviewed set after a successful scan, so the map cannot grow without bound.

3. **Retry decision.** For each PR the scan reports as unreviewed (it already passed the filters, trust check, override-label exclusion and the threshold):
   - `attempts < UnreviewedRetryMax` AND (`attempts == 0` OR `now − lastAttempt ≥ UnreviewedThreshold`) ⇒ re-publish via `TaskPublisher.PublishCreate(ctx, pr, taskID, details)` using the `PRDetails` the scan already fetched. On `true` increment `attempts`, set `lastAttempt = now`, increment the retry counter, and log `glog.Infof("unreviewed retry pr=%s/%s#%d sha=%s task_id=%s attempt=%d/%d", …)` (an operator-facing event, not `V`-gated — one line per retry, bounded by the budget). On `false` do not increment `attempts`; it is retried next cycle.
   - Otherwise ⇒ no publish this cycle.
   - The retry must never use the force path and never publish anything other than the create command.

4. **Metrics.**
   - New counter `github_pr_watcher_unreviewed_retries_total` with label `repo` (`owner/repo`), incremented once per successful re-publish. Register in `init()`.
   - Extend the unreviewed gauge from the previous prompt with a label `retries_exhausted` (`"true"` / `"false"`): `"true"` iff `attempts ≥ UnreviewedRetryMax` for that PR's task ID. With `UnreviewedRetryMax == 0` every unreviewed PR is `"true"` — no retry will help, so the alert should fire at the threshold. Keep the replace-whole-set-atomically semantics of the existing setter (update its signature and the mock accordingly).

5. **Tests (Ginkgo/Gomega, counterfeiter fakes, injected clock you can advance).** Cover at least:
   - first sighting past threshold ⇒ exactly one `PublishCreate` call with the `DeriveTaskID` value for that PR+SHA, counter +1, gauge `retries_exhausted="false"`
   - next scan before `UnreviewedThreshold` elapsed ⇒ no publish
   - advance the clock a threshold at a time ⇒ publishes 2 and 3, then a 4th scan publishes nothing and the gauge flips to `retries_exhausted="true"`
   - assert the task ID passed on every retry is identical (no force/nonce)
   - `PublishCreate` returns `false` ⇒ `attempts` not incremented, counter unchanged, retried next cycle
   - new head SHA for the same PR ⇒ fresh budget (publishes again)
   - PR reviewed (leaves the unreviewed set) ⇒ no publish and its map entry is pruned
   - `UnreviewedRetryMax = 0` ⇒ never publishes, gauge reports `retries_exhausted="true"`
   - `REVIEWER_BOT_LOGINS` empty ⇒ no publish, no GitHub calls (unchanged from the previous prompt)
   - one test against the real `prometheusMetrics` reading `github_pr_watcher_unreviewed_retries_total` and the `retries_exhausted` label back via `prometheus/client_golang/prometheus/testutil`

6. **Docs.** Document `UNREVIEWED_RETRY_MAX`, the retry behaviour, the new counter and the `retries_exhausted` label in the same places the previous prompt documented `UNREVIEWED_THRESHOLD` (README / docs). State in the docs that a retry cannot revive a task that already exists but never dispatched — only a new commit does — so an exhausted PR needs a human. Add a `## Unreleased` bullet to `CHANGELOG.md` starting `feat:`.

7. Before finishing, re-run the `<verification>` command and confirm it passes; then walk each requirement above and each test case in 5 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Retries publish ONLY through `TaskPublisher.PublishCreate` with `DeriveTaskID` — never `DeriveTaskIDForce`, never an override, never a label or any other GitHub mutation.
- At most `UnreviewedRetryMax` successful re-publishes per (PR, head SHA) per process lifetime.
- Behaviour with `REVIEWER_BOT_LOGINS` empty is byte-identical to before this prompt series.
- The normal poll path (`processPR`, cursor `HeadSHAs`, `processLabeledPRs`) is not modified; the retry lives in the scanner.
- No `time.Now()` in business logic — use the injected `libtime.CurrentDateTimeGetter`.
- Errors via `github.com/bborbe/errors`; Info logs `glog.V(n)`-gated except the single per-retry event line in requirement 3.
- Do not run `go mod vendor`.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.
</verification>
