# github-pr-watcher

Polls the GitHub Search API for open pull requests and publishes a `CreateTaskCommand` to Kafka
for each new or force-pushed PR so the [`github-pr-review-agent`](https://github.com/bborbe/github-pr-review-agent) picks it up automatically.

## Links

Dev:
https://dev.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/setloglevel/3
https://dev.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/check
https://dev.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/trigger?url=https://github.com/owner/repo/pull/123

Prod:
https://prod.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/setloglevel/3
https://prod.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/check
https://prod.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr/trigger?url=https://github.com/owner/repo/pull/123

## How It Works

The watcher runs a `user:<scope>` GitHub Search query on a configurable interval. On each poll
it compares the PR's current head SHA against the value stored in the cursor; if the SHA has
changed (force-push) the PR is re-submitted as a new task. The cursor is persisted to
`/data/cursor.json` between polls so a restart does not re-trigger every known PR.

Two independent decision chains run per PR — see [`docs/watcher-decision-chains.md`](https://github.com/bborbe/maintainer/blob/master/docs/watcher-decision-chains.md):

- **`TaskCreationFilter`** — should we create a task at all? (drafts, bots, WIP titles, age, allowlist)
- **`TrustGate`** — given a task is created, auto-process or route to `human_review`? (trusted authors)

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `GH_TOKEN` | yes | — | GitHub personal access token (read scope sufficient) |
| `KAFKA_BROKERS` | yes | — | Comma-separated Kafka broker list |
| `STAGE` | yes | — | Deployment stage (`dev` or `prod`) |
| `TRUSTED_AUTHORS` | yes | — | Comma-separated trusted GitHub logins; empty list refuses startup |
| `LISTEN` | no | `:9090` | HTTP listen address (`/healthz`, `/readiness`, `/metrics`, `/check`, `/trigger`) |
| `POLL_INTERVAL` | no | `5m` | Poll interval (Go duration string) |
| `REPO_SCOPE` | no | `bborbe` | GitHub user or org to search for PRs |
| `REPO_ALLOWLIST` | no | — | Comma-separated host-qualified repo allowlist (`host/owner/repo`); empty means allow-all |
| `BOT_ALLOWLIST` | no | `dependabot[bot],renovate[bot]` | Comma-separated bot author logins to skip |
| `MAX_PR_AGE` | no | `2160h` (90d) | Skip PRs older than this; empty disables |
| `BACKFILL_DURATION` | no | `720h` (30d) | On cold start, backdate the initial cursor by this; empty disables |
| `SENTRY_DSN` | no | — | Sentry DSN for error tracking |
| `SENTRY_PROXY` | no | — | HTTP proxy URL for Sentry transport |

### `REPO_ALLOWLIST` syntax

Entries are comma-separated. A leading `!` marks an exclusion. A target is allowed iff `(includes is empty OR any include matches) AND (no exclude matches)`; excludes always override includes.

| Entry shape | Example | Meaning |
|---|---|---|
| Literal include | `github.com/bborbe/maintainer` | Allow exactly this repo |
| Wildcard include | `github.com/bborbe/*` | Allow every repo under this owner |
| Literal exclude | `!github.com/bborbe/go-skeleton` | Reject exactly this repo (overrides any matching include) |
| Wildcard exclude | `!github.com/bborbe/*` | Reject every repo under this owner |

An allowlist consisting of only exclude entries is treated as allow-all-except: every target passes the include gate, and only the exclude gate filters. Example: `REPO_ALLOWLIST=!github.com/bborbe/go-skeleton` rejects go-skeleton and allows every other repo (including all other bborbe repos). To allow every bborbe repo except go-skeleton, write `github.com/bborbe/*,!github.com/bborbe/go-skeleton`.

## HTTP Endpoints

| Path | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Liveness probe (always returns 200 OK) |
| `/readiness` | GET | Readiness probe (always returns 200 OK) |
| `/metrics` | GET | Prometheus metrics |
| `/check` | POST | Run a poll cycle in the background; returns 200 immediately |
| `/trigger` | POST | Fire a single-PR review by URL (`?url=<pr_url>`); reuses the filter chain and trust evaluation |

### Single-PR Trigger Known Limit

If a vault task already exists for the same `(PR, SHA)`, the controller's `create-if-not-exists` is idempotent and no fresh agent Job spawns. To force a re-run in that case, reset the vault task's frontmatter (`phase`, `status`, `trigger_count`) manually OR push a new commit so the SHA changes.

## Development

```bash
make test          # run unit tests
make generate      # regenerate counterfeiter mocks
make precommit     # format + lint + test + security checks
```

## Cursor Mechanism

The cursor at `/data/cursor.json` records the timestamp of the most-recently-seen PR update plus
a map of `task_id → head_sha`. On cold start (missing file) the cursor is initialised to the
process start time minus `BACKFILL_DURATION`, so only PRs updated within that window are
submitted. Force-push detection compares the stored head SHA for a known PR against the value
returned by the current poll; a mismatch publishes a new `CreateTaskCommand` with the new SHA.

A corrupt cursor refuses startup — see `pkg/cursor.go`.

## Relationship to pr-reviewer

This service feeds tasks into the [`github-pr-review-agent`](https://github.com/bborbe/github-pr-review-agent) Pattern B Job via Kafka: for every new or
force-pushed PR it publishes a `CreateTaskCommand` that the agent task controller picks up and
spawns into a per-task K8s Job. The agent runs `/coding:pr-review` and posts the verdict back to
the PR.

See [`docs/architecture.md`](https://github.com/bborbe/maintainer/blob/master/docs/architecture.md) for the full pipeline.

## License

BSD 2-Clause License. See [LICENSE](LICENSE).
