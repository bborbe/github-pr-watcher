# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
