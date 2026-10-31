# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.20.0

- feat: add github_api_calls_total and github_api_rate_limited_total per-caller counters with request-derived bucket

## v0.19.0

- feat: log one `github non-2xx` line per non-2xx GitHub API response at the HTTP boundary (`rateCapturingTransport`), carrying `method`, `host`, `path`, `status`, `resource` (X-RateLimit-Resource), `remaining` (X-RateLimit-Remaining), `retry_after` and a body snippet truncated to 200 runes with `…`; a missing header or empty body is logged as the literal `absent`. Emitted at `glog.V(2)`, which the deployed `-v=2` shows. Without it a 429/403 cannot be split into primary-quota exhaustion and secondary rate limiting — go-github does not log at its HTTP boundary, contrary to the comment this replaces. The full URL and request headers are never logged; the response body is restored for the caller

## v0.18.5

- fix: a dep-bump PR retired as superseded is no longer then overridden. `trySupersedeDepBump` closes a dirty dep-bump PR so the update-go pipeline can replace it off current master, and its documented contract — "failures never block the review path: the caller continues regardless" — was written for a *failed* close. A **successful** close also fell through, so the same poll armed auto-merge against a closed PR and then published a mechanical override for it: an APPROVE on a PR that can never merge. Observed on `bborbe/agent-pi#36`, which logged `closed superseded dep-bump` at `17:29:48`, `enable auto-merge failed … Pull request is closed` at `17:29:49`, and `mechanical override … rule=trivial-bot-bump` at `17:29:50`. The function now reports whether it closed, and both callers stop: the labeled side-effect pass skips the branch-freshness and arming steps, and the poll path records the head in the cursor and returns handled. A failed close still returns false, so the review path is untouched and the old contract holds for the case it was written for

## v0.18.3

- fix: release the webhook's coalescer claim when the trigger executor defers a command to the poll path, so a head whose record was never published stops reporting `Settled`. `MarkEmitted` documents that callers invoke it "only after a successful publish", but the webhook handler claims the head as soon as its command reaches Kafka — and the consumer then declines that command for every mechanical-override author. The claim described a publish that never happened, and because `Admit` answered `Settled` for it the poll path published nothing either, so the head was stranded until the unreviewed scanner's threshold fired and spent the full LLM review the override exists to remove. Observed on `bborbe/backup#41`, which logged `already emitted` three minutes after creation, produced neither an override nor a review, and was rescued only by the scanner 30 minutes later — after which it merged on a full review. The deferral now calls the new `Coalescer.Release`, which clears only the claim it names, so a late release cannot unclaim a head that was published afterwards

## v0.18.1

- fix: a review is never published for a head SHA that a fresh `pr-override` already owns, whoever asked for it. The watcher has three producers of review tasks — the poll loop, the unreviewed scanner and the trigger executor — and only the poll loop owns the mechanical decision, so the other two spent exactly the reviewer pod the override exists to save. Observed on the first post-deploy population: `bborbe/kafka-topic-reader#26` got a review from the trigger executor **44 s after** its override, because the deferral is gated on `cmd.Webhook` and that command arrived with `Webhook=false`; `bborbe/alert#29` got one from the unreviewed scanner **30 min after** its override, while the APPROVE was still pending. The gate is on state, not origin: `pkg/override_ledger.go` records each published override against its head SHA in the same in-process store `RetryGuard` uses, and both publish chokepoints consult it — `TaskPublisher.PublishCreate` for the poll loop and the scanner, and `suppressForOverride` inside the command executor's own transaction for the webhook and `/trigger` paths. That covers every producer that exists and any added later, which an allow-list of origins cannot: the `#26` producer could not be identified at all. Suppression expires after one hour, derived rather than picked — prod pr-review Jobs carry `activeDeadlineSeconds: 1800`, so an override that has produced no APPROVE in that window never will, and without the bound a dead override would strand its PR unreviewed, which is worse than the duplicate it suppresses. Reads fail open for the same reason: a lost review is worse than a duplicate

## v0.18.0

- feat: bound how many review records a burst of pushes to one pull request can create, with a trailing coalesce in the new `pkg/coalesce` package. A six-push burst in seven minutes used to create five records — each one a pod start against a head that had already moved — and now creates at most two: a leading record dispatched immediately and a single trailing record emitted once the head has been quiet for `COALESCE_HOLD` (default `3m`). The leading edge is unconditional on purpose: a full debounce would reach one record but must hold the first push too, which adds the hold window to every PR including a lone one, and the measured p50 is 36 s — a 60 s hold roughly doubles it and pushes the review-latency p90 from 10.0 m past the 10-minute bar the parent goal is chasing. One coalescer instance is shared by the poll loop and the webhook receiver, because gating either alone still lets the other emit a third record: the webhook fires per push, and the poll fires per unseen (PR, SHA). `Admit` returns a three-valued `Decision`, not a bool, because "held for the trailing dispatch" and "already published for this head" demand opposite cursor handling — the first must not advance the poll cursor, the second must, or the `updated:>=cursor` search window pins to that PR for the process lifetime and every PR in it is re-fetched with its side effects re-run each poll. Nothing is recorded as published until the caller confirms with `MarkEmitted`, so a failed send leaves the head claimable by the next webhook retry or poll instead of being recorded as done and lost. Only `synchronize` deliveries are coalesced: `opened`, `reopened` and `ready_for_review` are state transitions that may carry an unchanged head SHA, and gating them on the head would swallow the transition after the push that preceded it — a draft PR's push is dispatched and dropped by the consumer's draft gate, then `ready_for_review` arrives with the same SHA and would publish nothing at all. The trailing dispatch republishes the identical `TriggerPRReviewCommand` (including `Webhook: true`, which the consumer reads to defer a mechanical-override author to the poll path) and counts `result="trailing"`; a coalesced webhook delivery counts `result="coalesced"`. The behaviour is measured against `bborbe-nuke#815`, whose eight runs on 2026-10-27 produced five review records from six pushes in seven minutes, three of which ran a full review and five of which were moot on arrival

## v0.17.2

- fix: park bodies describe a working un-park lever (forced `/trigger` for oversized, `TRUSTED_AUTHORS` for untrusted) instead of a frontmatter edit the reviewer always fails, because the URL-less park body cannot be reviewed; the README names the same levers. Both bodies and the README also state that a forced run is suppressed for up to an hour after the park — the parked publish is recorded in the retry guard's ledger and the SHA carries no verdict, so the guard skips it after the endpoint has already answered 202 — and name waiting out that hour or restarting the watcher as the way past it, so the lever is never presented as immediately effective
- chore: update golang.org/x/net to v0.62.0, clearing GO-2026-6860 and GO-2026-6866 (quadratic text-node coalescing and missing ancestor namespace transitions in `html`), which the `vulncheck` precommit gate reports as reachable

## v0.17.1

- fix: a review task created for a **fork PR** now carries the **base** repo's `clone_url` instead of the contributor's fork, so the record names a repo that actually holds the pull request. `GetPRDetails` populated `CloneURL` from `pr.head.repo.clone_url`, which on a fork PR is the fork; the PR number does not exist there, so a reader holding only the record's URL and number gets *"Could not resolve to a PullRequest with the number of N"* and the record can never resolve to a verdict. Observed on `bborbe/ai-assistant#106` from the `YetAnotherGerrit/ai-assistant` fork: `gh api repos/YetAnotherGerrit/ai-assistant` succeeds (the repo exists), `gh pr view 106` in it fails (the PR does not), so nothing looks broken at either end — the mismatch is only visible when the two halves are checked together. `HeadRepoFullName` still names the head repo, because `trust.NewSameRepo()` depends on it; only the clone URL changes hands, and `BaseRepoFullName` was already correct. The test fixture that covered this case was itself wrong — its `head.repo.clone_url` read `https://github.com/owner/repo.git` beside a `full_name` of `forkowner/repo`, and its `base.repo` carried no `clone_url` key at all — so the base accessor could never have been distinguished from the head one; it is replaced by a case where the two URLs differ, plus a captured real `pulls/106` response served verbatim

## v0.17.0

- fix: a mechanical-override gate that **fails to evaluate** no longer falls through to a full LLM review. `qualifiesForMechanicalOverride` returns a three-valued `overrideQualification` instead of a `bool`, separating "does not qualify" (settled — review as before) from "could not determine" (a GitHub read failed — publish nothing and re-evaluate next poll; a deferral writes no cursor entry, so the poll cursor is held at the deferred PR rather than advancing past it, which keeps the PR inside the next `updated:>=cursor` window — otherwise any other PR updated later in the same batch would push the cursor beyond it and the PR would receive neither a review nor an override, since the unreviewed scanner is off unless `REVIEWER_BOT_LOGINS` is configured; the retry is bounded to three polls, after which the PR takes the review rather than being starved). Previously both returned `false`, and the fall-through was permanent: a review published for that SHA writes a review-task cursor entry, and the review-already-emitted guard then blocks the override for that SHA forever, so one failed read cost a full review for the life of the commit. Observed on `bborbe/errors#22`, reviewed two minutes after creation, never overridden, and merged on that review — one reviewer pod spent on exactly the trivial bump the override exists to cover. The indeterminate branch logs at default verbosity so the next occurrence names itself instead of requiring this reconstruction
- fix: suppress a forced re-review while a review for the same head SHA is still in flight, so an operator retry no longer publishes a second task alongside the running one — the forced path consults an in-memory ledger of published reviews plus a reviewer-verdict lookup, counts a suppression under the new `inflight_skipped` label on `github_pr_watcher_prs_total`, and still re-runs after a verdict or once the one-hour staleness bound passes. The ledger records every publish that creates a review task, the poll loop and the unreviewed scanner included, so retrying the review the poll path published is suppressed too; startup now fails loudly when `REVIEWER_BOT_LOGINS` is empty, because the verdict check that lets an operator re-run a finished review is dead without it

## v0.16.0

- feat: require a pull request's head repository to match its base repository before trusting it, closing the fork-PR trust hole — a workflow running in a fork that opens a PR against upstream renders as the same author login, so the trusted-author list alone would have let a fork PR be auto-reviewed. `trust.PR` and `PRDetails` gain `HeadRepoFullName`/`BaseRepoFullName`, populated from `pr.head.repo.full_name` and `pr.base.repo.full_name` in `GetPRDetails`; the new `trust.NewSameRepo()` leaf denies an unknown head or base repo (fail-closed) and is registered in the production `trust.And` chain shared by the watcher, the command consumer and the single-PR trigger, so a fork PR is denied on every path including an explicit `/trigger` (non-retryable `ErrCommandObjectSkipped`). The unreviewed-PR scan now runs its trust check after the details fetch, because the head repo only arrives with the details

## v0.15.1

- fix: defer a **webhook**-published review to the poll path when the PR author is on `MECHANICAL_OVERRIDE_AUTHORS`, so a trivial bot bump no longer receives both an LLM review and a mechanical override on the same head SHA — the webhook arrives seconds after the PR is created, always before the poll path can act, and the poll path owns the override-vs-review decision. `TriggerPRReviewCommand` gains a `webhook` flag set only by the webhook handler, so every operator command on the `/trigger` endpoint — forced or not — still gets a full review; a deferral is counted under a new `deferred` label on `github_pr_watcher_prs_total` rather than `skipped`, which keeps meaning "dropped"

## v0.15.0

- feat: mechanically approve trivial bot dependency bumps on opted-in repos, replacing the LLM review for that change class. A pull request qualifies when its author is on `MECHANICAL_OVERRIDE_AUTHORS`, the author is trusted, the repo's `.maintainer.yaml` carries `autoMerge.trivial` + `prReviewer.autoApprove`, and every changed file is mechanical; a qualifying PR gets the auto-merge label, has native auto-merge armed, and receives one `pr-override` task carrying `override_reason: trivial-bot-bump` and a body naming the rule and stating that no LLM review ran — deduplicated per (PR, head SHA) on the existing override cursor entry, never alongside a review task for that SHA, retried next poll when the send fails, and recorded under the new `override_mechanical` label on `github_pr_watcher_prs_total` (pre-created at zero). A PR carrying the override label keeps the label override, which takes precedence
- fix: classify a dependency bump whose only changes are paired go.sum hash lines, go.mod version lines and an appended CHANGELOG line as trivial
- feat: classify a single-hash homebrew cask bump whose only changes are one paired `version` line and one paired `sha256` line as trivial, while multi-architecture casks and any other cask edit stay non-mechanical

## v0.14.3

- chore: update Go to 1.27.3 and github.com/bborbe/agent to v0.112.0, github.com/bborbe/kafka to v1.28.4, github.com/bborbe/maintainer to v0.50.12, github.com/bborbe/sentry to v1.10.5, github.com/bborbe/service to v1.11.0, github.com/bborbe/time to v1.27.17, github.com/bborbe/validation to v1.5.5

## v0.14.2

- chore: update github.com/bborbe/agent to v0.101.0, github.com/bborbe/http to v1.26.30

## v0.14.1

- chore: update github.com/bborbe/http to v1.26.29, github.com/bborbe/kafka to v1.27.1, github.com/bborbe/kv to v1.21.18, github.com/bborbe/maintainer to v0.50.11, github.com/bborbe/parse to v1.12.3, github.com/bborbe/sentry to v1.10.4, github.com/bborbe/service to v1.10.16, github.com/bborbe/validation to v1.5.4

## v0.14.0

- chore: add dark-factory setup (`.dark-factory.yaml`, `docs/dod.md`, `prompts/` + `specs/` dirs, `.gitignore` lock/log entries, `.reviewignore` exclusion of `prompts/` + `specs/`) copied from `go-skeleton`, and align `.maintainer.yaml` with it (`release.changelogRewrite: false`) — code changes now go through the prompt flow like the rest of the fleet
- feat: report open PRs still waiting for a reviewer-bot review on a new `github_pr_watcher_unreviewed_prs` gauge (labels `repo`, `pr`), rescanned once per poll over every open PR the watcher would publish — the end-to-end "review never arrived" signal missing from the 2026-10-12 incident, where two review commands were lost downstream and every existing counter still looked healthy. Adds `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds` so a scan that keeps failing is visible, and configures the reviewer identities and wait via `REVIEWER_BOT_LOGINS` (empty disables the scan entirely) and `UNREVIEWED_THRESHOLD` (default `30m`)
- feat: heal a lost review by re-publishing the still-unreviewed PR's original review task — same deterministic `DeriveTaskID`, same create command, up to `UNREVIEWED_RETRY_MAX` times (default `3`, `0` disables) one `UNREVIEWED_THRESHOLD` apart per (PR, head SHA). The retry pass runs only after a complete scan and never mutates GitHub; each re-send increments the new `github_pr_watcher_unreviewed_retries_total{repo}` counter and logs one `unreviewed retry …` line. The `github_pr_watcher_unreviewed_prs` gauge gains a `retries_exhausted` label, `"true"` once the budget is used up and the last retry has had a full threshold without a review, so the alert fires only when retrying did not help
- fix: harden the unreviewed-PR scan — a failing trust check now aborts the scan like every other lookup failure instead of publishing a partial `github_pr_watcher_unreviewed_prs` set, an overlapping scan is skipped rather than queued behind the running one, and PRs whose head SHA is already confirmed reviewed are cached until their `UpdatedAt` moves, so a settled PR no longer costs two GitHub calls per poll on the shared App token. `UNREVIEWED_THRESHOLD` is now ignored (and no longer validated) when `REVIEWER_BOT_LOGINS` is empty, matching `UNREVIEWED_RETRY_MAX`

## v0.13.2

- chore: update Go to 1.27.2 and github.com/bborbe/agent to v0.100.0, github.com/bborbe/cqrs to v0.6.12, github.com/bborbe/errors to v1.6.3, github.com/bborbe/http to v1.26.28, github.com/bborbe/kafka to v1.27.0, github.com/bborbe/kv to v1.21.17, github.com/bborbe/log to v1.7.4, github.com/bborbe/maintainer to v0.50.9, github.com/bborbe/parse to v1.12.2, github.com/bborbe/run to v1.11.2, github.com/bborbe/sentry to v1.10.3, github.com/bborbe/service to v1.10.15, github.com/bborbe/time to v1.27.16, github.com/bborbe/validation to v1.5.3, github.com/onsi/ginkgo/v2 to v2.33.1, github.com/onsi/gomega to v1.44.0, github.com/prometheus/client_golang to v1.25.0

## v0.13.1

- fix: bump `osv-scanner` to v2.6.0 and `golang.org/x/net` to v0.60.0 so the Linux vulnerability gates stop failing. v2.3.1 pins `golang.org/x/tools` v0.38.0, whose SSA builder aborts with `unexpected expr: *ast.KeyValueExpr` on the promoted-field composite-literal key Go 1.27 permits in the Linux stdlib, so a repo on the old pin passes locally on darwin and fails only in Linux CI. `x/net` v0.58.0 carries `GO-2026-6603/6610/6611/6612/6617`, which fail both `vulncheck` and `trivy`.

## v0.13.0

- feat: name the filter that skipped a PR in the `filtered` log line — one undifferentiated `trigger executor: filtered pr=…` served all five skip causes, so attributing a skip meant eliminating the other four by hand. The line now carries `reason=<draft|bot-author|wip-title|age|repo-allowlist>`, resolved through a new `TaskCreationFilters.SkippingFilter` that names the first voter — the only one the short-circuit chain can reach
- feat: `Name() string` on the `TaskCreationFilter` interface, mandatory rather than an optional secondary interface, so a future filter cannot skip silently without naming itself — the exact defect this fixes

## v0.12.1

- fix: run the auto-merge side effects — update-branch, auto-merge, supersede — over the auto-merge-labeled PRs each poll, not only over the PRs the cursor window returns. That window is `updated:>=cursor` and the cursor only ever advances, so a PR that stopped being updated (which is what a stale branch IS) left it permanently, making the side effects unreachable for exactly the population they exist to serve
- fix: `SearchLabeledPRs` on the GitHub client — a label-scoped search with no `updated:` bound, so a labeled PR stays reachable however old it is
- fix: bound that pass with a `sideEffectFilter` rather than the task-creation filter — the repo-allowlist and draft gates still apply (a dev watcher must not act on repos outside its allowlist, whatever labels they carry), the age/bot/WIP gates do not

## v0.12.0

- feat: retire a conflicting update-go dep-bump PR instead of trying to refresh it — `trySupersedeDepBump` closes it with a comment explaining the CHANGELOG fold race, so the update-go pipeline replaces it with a fresh PR off current master
- feat: `ClosePR` on the GitHub client — REST close plus a best-effort comment, so a comment failure cannot strand the stale PR and its conflicting CHANGELOG hunk
- feat: carry `HeadRef` on `PRDetails`. A superseded dep-bump is `fix/update-go-` + a `[bot]` author + `dirty` — a discriminator verified across five live PRs; the author check is what keeps a human's PR from ever being auto-closed, however its branch is named
- fix: `tryUpdateBranch` skips the dirty-dep-bump case so the abort path owns it outright, rather than attempting an update-branch that cannot succeed

## v0.11.0

- feat: add `tryUpdateBranch` — a trusted author's auto-merge-labeled PR whose head branch has gone stale against its base now gets GitHub-native update-branch (a merge, never a force-push) before auto-merge is re-armed, so master advancing no longer strands an otherwise-mergeable PR
- feat: carry `MergeableState` on `PRDetails` (from the `PullRequests.Get` already made per poll), gating the update on `behind`/`dirty` and deliberately not on `unknown`, which would fire an update every poll until GitHub finishes computing the state
- fix: a conflicting update (422) is logged and counted, never fatal — update-branch resolves staleness, not conflicts, so the poll and the review path continue unchanged

## v0.10.0

- feat: honour a repo-root `.reviewignore` (gitignore syntax) so non-reviewable paths — vendored deps, generated mocks, dark-factory `prompts/`/`specs/` — no longer count toward the `MAX_ADDITIONS`/`MAX_CHANGED_FILES` park thresholds
- feat: report `N additions across M files excluded by .reviewignore` on both the parked body and the normal review task body, so an exclusion is always visible
- feat: `.reviewignore` can never exclude itself; its own added lines always count toward the size gate
- fix: follow git's rule that a file cannot be re-included once a parent directory is excluded (`specs/` + `!specs/README.md` keeps README excluded) — the gitignore library does not implement this, and the size gate must agree with the reviewer, which uses git's own engine

## v0.9.4

- chore: update Go to 1.27.1

## v0.9.3

- chore: update github.com/bborbe/agent to v0.87.1, github.com/bborbe/kafka to v1.25.12, github.com/bborbe/maintainer to v0.50.6, github.com/bborbe/service to v1.10.12

## v0.9.2

- chore: update go module dependencies

## v0.9.1

- chore: update github.com/bborbe/agent to v0.87.0, github.com/bborbe/cqrs to v0.6.10, github.com/bborbe/errors to v1.6.0, github.com/bborbe/http to v1.26.25, github.com/bborbe/kafka to v1.25.11, github.com/bborbe/kv to v1.21.13, github.com/bborbe/maintainer to v0.50.5, github.com/bborbe/parse to v1.11.1, github.com/bborbe/run to v1.10.2, github.com/bborbe/sentry to v1.10.1, github.com/bborbe/service to v1.10.11, github.com/bborbe/time to v1.27.12, github.com/onsi/gomega to v1.43.0

## v0.9.0

- feat: opt into `autoMerge.trivial` for mechanically-trivial update PRs

## v0.8.6

- fix: park oversized PRs at human_review instead of spawning a review pod whose prompt overflows the context window (MAX_ADDITIONS / MAX_CHANGED_FILES thresholds)

## v0.8.5

- chore: update github.com/bborbe/agent to v0.83.1, github.com/bborbe/maintainer to v0.50.3, github.com/bborbe/validation to v1.4.23

## v0.8.4

- chore: update github.com/bborbe/errors to v1.5.21, github.com/bborbe/log to v1.6.25, github.com/bborbe/maintainer to v0.50.2

## v0.8.3

- chore: update github.com/bborbe/agent to v0.83.0, github.com/bborbe/http to v1.26.24, github.com/bborbe/log to v1.6.24, github.com/bborbe/maintainer to v0.50.1, github.com/bborbe/sentry to v1.9.27, github.com/bborbe/validation to v1.4.22

## v0.8.2

- fix: populate the `rate_limit_remaining` gauge from `GET /rate_limit` (core bucket) on poll cycles that make no core-API call — a search-only cycle with no open PRs previously left the gauge at the 0 "unpopulated" sentinel, which falsely read as quota exhaustion (found live on nukedev after the v0.8.1 core-scoping fix; the 0-gauge + `RateLimitRemainingCritical` < 500 rule re-fires)

## v0.8.1

- fix: scope the `rate_limit_remaining` gauge capture to the GitHub **core** rate-limit bucket — Search API responses report their own 30/min `search` bucket, which overwrote the gauge with a low number while the shared primary token was healthy (the false "29 remaining" critical alarm on both dev and prod, 2026-08-24)

## v0.8.0

- feat: `github_pr_watcher_rate_limit_remaining` gauge — exposes the shared App token's primary rate-limit remaining (from `X-RateLimit-Remaining`, captured at the transport layer) after every poll, the alert surface for quota exhaustion before the fleet-wide 403 stall (2026-08-23)

## v0.7.0

- feat: automatically apply the `auto-merge` label to mechanically trivial PRs on repos opted in via `.maintainer.yaml` `autoMerge.trivial` (`TRIVIAL_AUTO_MERGE`; stage-1 allow-list, LLM stage-2 follow-up)

## v0.6.1

- chore: update go module dependencies

## v0.6.0

- feat: add `/webhook/github-pr` GitHub webhook receiver — HMAC-verified `pull_request` deliveries publish a `TriggerPRReviewCommand` so bot review starts within seconds of a push instead of waiting for the next 5-minute poll. Polling and the manual `/trigger` remain as fallbacks. New metrics: `webhook_deliveries_total`, `webhook_signature_rejections_total`, `webhook_dispatch_latency_seconds`.

## v0.5.4

- chore: Bump errcheck to v1.20.0 and golangci-lint to v2.13.1 for Go 1.27 support
## v0.5.3

- docs: document the auto-merge arming window in the README — arming uses the GraphQL `enablePullRequestAutoMerge` mutation (no REST route exists), and it is rejected with `UNPROCESSABLE: Pull request is in clean status` on a pull request that is already mergeable. Explains why a fast-turnaround pull request can go unarmed, and why that is expected rather than a fault.

## v0.5.2

- fix: `EnableAutoMerge` calls the GraphQL `enablePullRequestAutoMerge` mutation instead of a REST route that does not exist. `PUT /repos/{owner}/{repo}/pulls/{number}/auto-merge` returns 404 Not Found — GitHub exposes auto-merge only via GraphQL, which is why go-github has no typed wrapper for it. Every arming attempt since the feature shipped failed with that 404. The mutation needs the PR's GraphQL node id, so the PR is fetched first, and because GitHub answers a failed mutation with HTTP 200 plus a non-empty `errors` array, the response body is now inspected rather than trusting a nil transport error.
- fix: document that arming requires the PR to still be blocked. GraphQL rejects an already-mergeable PR with UNPROCESSABLE "Pull request is in clean status", so a PR that has gone green and approved before the watcher's poll reaches it can no longer be armed.

## v0.5.1

- fix: `make build` refuses to stamp a version onto a tree that is not that version's tag (`check-version-tag`, escape hatch `ALLOW_UNTAGGED_BUILD=1`). `VERSION` defaults to the newest tag in the repo regardless of what is checked out, so an operator-run build from master silently stamps the newest tag's number onto whatever tree is present. This shipped a bad `v0.5.0` image: the tag contains `AutoMergeLabel` + `tryAutoMerge`, but the published image was built from a pre-merge tree, so the deployed watcher had no auto-merge code at all — its startup argument dump printed `OverrideLabel` and no `AutoMergeLabel`. Nothing surfaced it: the tag, the changelog and the image name all agreed, and the arming path failed silently because the label check logs nothing when it misses.
- fix: stamp `BUILD_GIT_VERSION` (`git describe --tags --always --dirty`) into the image as a build arg and `ENV`, alongside the existing `BUILD_GIT_COMMIT` / `BUILD_DATE`. Makes a published image self-identifying, so the stale-image drift above is detectable by inspecting the image instead of diffing a running pod's startup config against the source tree.

## v0.5.0

- feat: arm GitHub-native auto-merge (`auto-merge` label, `AUTO_MERGE_LABEL`) for trusted authors so PRs merge once checks + required reviews are green; add `EnableAutoMerge` to the GitHub client; requires the watcher App to hold Pull requests: Write

## v0.4.0

- feat: echo the `force` flag in the `/trigger` 202 response (`force` appears only when `true`, omitted otherwise)

## v0.3.2

- chore: bump Go 1.26.5 → 1.26.6 and update dependencies; fixes GO-2026-6179 and GO-2026-6180 (golang.org/x/mod)

## v0.3.1

- fix: make a forced re-review (`trigger-pr-review` with `force: true`) actually land. `DeriveTaskIDForce` salted only the `task_identifier`, but the agent controller dedupes on the **title path** (`checkTitlePathFree`), which is SHA-scoped and nonce-free — so every forced re-review of an unchanged head SHA resolved to the existing review task's filename and was silently rejected with `ErrTaskAlreadyExists`, while the watcher had already counted it as `github_pr_published{result="create"}` and the gateway had returned a valid `requestID` and exit 0. `BuildCreateCommand` now takes a `forced` flag and appends a ` - retry-<taskid[:8]>` segment to the title, riding the existing task-suffix truncation budget. Non-forced titles are byte-identical to before. Confirmed against prod on `bborbe/coding#90` (2026-08-09)
- fix(logging): stop logging-and-returning the same error in the trigger executor's trust-check and create-task-send paths. Both wrapped and returned the error *and* logged it with `glog.Errorf`, so the cdb boundary logged it a second time. The `glog.Errorf` calls are dropped and the returns kept — returning is what makes the framework emit a Failure and Kafka redeliver, so dropping the return instead would have silently swallowed transient errors
- chore: bump alpine base image to 3.24; update bborbe/* dependencies and the prometheus, go-openapi, klauspost, golang.org/x and k8s.io transitive dependencies. Clears the two advisories that were failing `vulncheck` on master: GO-2026-5841 (`klauspost/compress` → v1.19.0, OOB read in s2) and GO-2026-5970 (`golang.org/x/text` → v0.40.0, infinite loop on invalid input)

## v0.3.0

- feat: add `override-review` label support. When a trusted author applies the `override-review` label (configurable via `OVERRIDE_REVIEW_LABEL`, default `override-review`) to a PR, the watcher emits a `pr-override` task *instead of* a review task for that head SHA, with a distinct deterministic task-id (`DeriveTaskIDOverride`) so it never collides with the review task's dedup. The pr-review agent then posts an APPROVE at head SHA, clearing a false-positive review without admin-merge. Emitting override-only avoids an APPROVE-vs-CHANGES_REQUESTED race. Untrusted authors are skipped (fall through to the normal review path → `human_review`). PR labels are now read from the Search + Get APIs (new `Labels` field on `PullRequest`/`PRDetails`).

## v0.2.0

- feat: add optional `--target-vault` / `TARGET_VAULT` flag. When set, the watcher stamps `TargetVault` on every emitted `CreateTaskCommand`, so it routes to a controller whose `VAULT_NAME` matches verbatim. Empty (default) leaves `TargetVault` unset, preserving the controller's legacy default-vault fallback — existing deployments are byte-compatible on the wire. Enables deployments whose work-vault is not the controller's hardcoded legacy default (e.g. the Seibert-Data `agent` vault).
- chore: bump Go 1.26.4 → 1.26.5 (go.mod + Dockerfile) to clear stdlib advisory GO-2026-5856; ignore unmaintained-openpgp advisory GO-2026-5932 in `VULNCHECK_IGNORE` (indirect, unreachable, no fix — same class as the existing GO-2022-0470 ignore).

## v0.1.1

- refactor: import the shared library from its new root module path `github.com/bborbe/maintainer` (was `github.com/bborbe/maintainer/lib`) and bump to `@v0.45.0`. The maintainer repo flattened `lib/` to its root to match the `bborbe/agent` layout. No behavior change.

## v0.1.0

- Extracted from the `bborbe/maintainer` monorepo (`watcher/github-pr`) into a standalone
  publish-only repository. Shared code now comes from the versioned
  `github.com/bborbe/maintainer/lib` module instead of a local `replace`. Builds and
  publishes `docker.io/bborbe/github-pr-watcher:<version>` via `make buca`.
