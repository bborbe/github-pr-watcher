# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

- feat: add optional `--target-vault` / `TARGET_VAULT` flag. When set, the watcher stamps `TargetVault` on every emitted `CreateTaskCommand`, so it routes to a controller whose `VAULT_NAME` matches verbatim. Empty (default) leaves `TargetVault` unset, preserving the controller's legacy default-vault fallback — existing deployments are byte-compatible on the wire. Enables deployments whose work-vault is not the controller's hardcoded legacy default (e.g. the Seibert-Data `agent` vault).
- chore: bump Go 1.26.4 → 1.26.5 (go.mod + Dockerfile) to clear stdlib advisory GO-2026-5856; ignore unmaintained-openpgp advisory GO-2026-5932 in `VULNCHECK_IGNORE` (indirect, unreachable, no fix — same class as the existing GO-2022-0470 ignore).

## v0.1.1

- refactor: import the shared library from its new root module path `github.com/bborbe/maintainer` (was `github.com/bborbe/maintainer/lib`) and bump to `@v0.45.0`. The maintainer repo flattened `lib/` to its root to match the `bborbe/agent` layout. No behavior change.

## v0.1.0

- Extracted from the `bborbe/maintainer` monorepo (`watcher/github-pr`) into a standalone
  publish-only repository. Shared code now comes from the versioned
  `github.com/bborbe/maintainer/lib` module instead of a local `replace`. Builds and
  publishes `docker.io/bborbe/github-pr-watcher:<version>` via `make buca`.
