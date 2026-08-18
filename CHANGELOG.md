# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

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
