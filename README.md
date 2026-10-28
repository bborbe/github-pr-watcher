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
| `GH_TOKEN` | yes | — | GitHub personal access token (read scope sufficient for search/review; **Pull requests: Write required for auto-merge arming** — the deployed App must be bumped, see `AUTO_MERGE_LABEL`) |
| `KAFKA_BROKERS` | yes | — | Comma-separated Kafka broker list |
| `STAGE` | yes | — | Deployment stage (`dev` or `prod`) |
| `TRUSTED_AUTHORS` | yes | — | Comma-separated trusted GitHub logins; empty list refuses startup |
| `LISTEN` | no | `:9090` | HTTP listen address (`/healthz`, `/readiness`, `/metrics`, `/check`, `/trigger`) |
| `POLL_INTERVAL` | no | `5m` | Poll interval (Go duration string) |
| `REPO_SCOPE` | no | `bborbe` | GitHub user or org to search for PRs |
| `REPO_ALLOWLIST` | no | — | Comma-separated host-qualified repo allowlist (`host/owner/repo`); empty means allow-all |
| `BOT_ALLOWLIST` | no | `dependabot[bot],renovate[bot]` | Comma-separated bot author logins to skip |
| `AUTO_MERGE_LABEL` | no | `auto-merge` | PR label that opts the PR into GitHub-native auto-merge for trusted authors (watcher arms `EnableAutoMerge`; GitHub merges once checks + required reviews are green). Empty disables the auto-merge path. Requires the watcher GitHub App to hold Pull requests: Write. **Arming only works while the PR is still blocked** — see below |
| `MECHANICAL_OVERRIDE_AUTHORS` | no | — | Comma-separated GitHub logins that may be approved without an LLM review on a repo that opts in through **both** `.maintainer.yaml` flags (`autoMerge.trivial` + `prReviewer.autoApprove`). Exact, case-sensitive match. Empty disables the path. Not the same list as `BOT_ALLOWLIST` (skip) or `TRUSTED_AUTHORS` (may be reviewed) — a login here must also be trusted |
| `MAX_ADDITIONS` | no | `0` | Park PRs with more added lines than this at `human_review` instead of spawning a review pod (an oversized diff overflows the reviewer's context window — `claude CLI failed: Prompt is too long`); `0` disables |
| `MAX_CHANGED_FILES` | no | `0` | Park PRs touching more files than this at `human_review` instead of spawning a review pod; `0` disables |
| `MAX_PR_AGE` | no | `2160h` (90d) | Skip PRs older than this; empty disables |
| `BACKFILL_DURATION` | no | `720h` (30d) | On cold start, backdate the initial cursor by this; empty disables |
| `REVIEWER_BOT_LOGINS` | yes | — | Comma-separated GitHub logins of the reviewer bots. A publishable PR whose current head SHA has no review from one of them, after waiting longer than `UNREVIEWED_THRESHOLD`, is reported on `github_pr_watcher_unreviewed_prs`. Required: the retry guard's verdict check cannot tell a finished review from an in-flight one without it, so the watcher refuses to start when it is empty |
| `UNREVIEWED_THRESHOLD` | no | `30m` | How long a PR must wait without a reviewer-bot review before it is reported (Go duration; must be positive), and how long to wait between retries of an unreviewed PR. Ignored (and not validated) when `REVIEWER_BOT_LOGINS` is empty |
| `UNREVIEWED_RETRY_MAX` | no | `3` | How many times the scan re-publishes the original review task for a still-unreviewed PR, one threshold apart, before marking it `retries_exhausted`; `0` disables retrying. Ignored (and not range-validated) when `REVIEWER_BOT_LOGINS` is empty |
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

## `.reviewignore` — per-repo size-gate exclusion

`MAX_ADDITIONS` / `MAX_CHANGED_FILES` count the whole diff, so a PR that is mostly
non-reviewable content parks even when the real code change is small. A repo opts
out of that by committing a `.reviewignore` at its root:

```gitignore
# generated / vendored — nobody reviews these
vendor/
**/mocks/**
*.snap

# dark-factory pipeline state (NOT scenarios — those are shipped contracts)
prompts/
specs/
```

Syntax is gitignore: `#` comments, blank lines, `**` depth wildcards, `!` negation.
Matched files are subtracted from both the added-line and changed-file counts
**before** the park decision, so only reviewable content can trip the thresholds.
The same exclusion is reported on the task body:

```
499 additions across 3 files excluded by `.reviewignore` (not counted toward the size gate, not sent to the reviewer).
```

Two properties are deliberate:

- **The audit line appears on every review task, not only parked ones.** Reporting it
  only on parks would hide the exclusion on exactly the PRs it rescued from parking.
- **`.reviewignore` can never exclude itself.** A pattern matching `.reviewignore` —
  directly, via `*`, or via negation ordering — has no effect: the file's own added
  lines always count toward the gate and never appear in the excluded total. A repo
  cannot use the file to conceal edits to the file.

An absent or empty `.reviewignore` excludes nothing, and any error reading it is
treated the same way — a failure can only ever park more, never let an oversized PR
through.

### Negation and excluded parents

Matching follows git, including the gitignore rule that **a file cannot be
re-included once a parent directory is excluded**:

```gitignore
specs/
!specs/README.md    # has NO effect — specs/ already excluded the parent
```

`specs/README.md` stays excluded. To exclude a directory's contents while keeping
one file, exclude by pattern rather than by directory:

```gitignore
specs/*
!specs/README.md    # works — no parent directory was excluded
```

This is worth stating because the Go gitignore library backing the size gate does
*not* implement that rule on its own; the matcher re-checks ancestor directories to
restore it. The size gate and the reviewer prompt (which drives git's own ignore
engine) must agree about the same file — otherwise a path the gate counts as
reviewable would be withheld from the reviewer.

## Metrics

All metrics use the `github_pr_watcher_` prefix and are exposed on `/metrics`.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `github_pr_watcher_unreviewed_prs` | gauge | `repo` (`owner/repo`), `pr` (number), `retries_exhausted` (`true`/`false`) | `1` for every open, publishable, trusted-author PR whose current head SHA has no review from a configured reviewer bot after waiting longer than `UNREVIEWED_THRESHOLD`. The PR leaves the gauge as soon as a matching review lands on its head SHA, or when it stops qualifying (closed, drafted, untrusted, oversized-parked, …). `retries_exhausted="true"` means the retry budget is used up AND the last retry has had a full threshold without a review — the alert fires on that |
| `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds` | gauge | — | Unix seconds of the last complete unreviewed-PR scan. `0` until the first one — alert rules on it must guard `> 0`, which is how a scan that keeps failing becomes visible |
| `github_pr_watcher_unreviewed_retries_total` | counter | `repo` (`owner/repo`) | Review tasks re-published by the retry pass. Bounded by `UNREVIEWED_RETRY_MAX` per (PR, head SHA) per process lifetime. A retry is a re-send, not proof the review arrived — pair it with the gauge |

The scan runs once per poll cycle, after the cursor is saved and before the cycle
is counted successful, so a slow or failing scan never changes the poll result.
A failed or interrupted scan leaves the previous gauge values in place; only a
complete scan replaces them (an empty result clears the gauge).
`REVIEWER_BOT_LOGINS` is required: the retry guard's verdict check shares it, so
an empty value fails startup instead of silently disabling both the scan and the
guard's ability to re-run a review that already posted a verdict.

A PR whose head SHA has been confirmed reviewed is remembered until it changes,
so a settled PR costs no `GetPRDetails`/`ListReviews` calls on later scans. Any
push, draft toggle, comment or label moves the PR's `UpdatedAt` and forces a
fresh check.

### Unreviewed retry

When a scan finds a PR still unreviewed, it re-publishes that PR's original
review task — the same `DeriveTaskID` and the same create command the poll would
send — up to `UNREVIEWED_RETRY_MAX` times per (PR, head SHA), one
`UNREVIEWED_THRESHOLD` apart. A new commit is a new head SHA and starts a fresh
budget; a pod restart can grant up to `UNREVIEWED_RETRY_MAX` more. The retry
only ever re-sends the create command: it never forces a re-review, never
mutates GitHub, and only fires after a complete, successful scan. Each retry
logs one `unreviewed retry …` line and increments
`github_pr_watcher_unreviewed_retries_total`.

Known limits:

- A retry cannot revive a task that already exists but never dispatched — only a
  new commit does. An exhausted PR needs a human.
- The agent controller dedups on the task **title path**, which embeds the
  slugified PR title, so editing a PR's title between the original publish and a
  retry can produce a second review task for the same commit.
- Each retry also increments `github_pr_watcher_prs_total{command="create"}` and
  may cost 1–2 extra GitHub calls (`PublishCreate`'s `.reviewignore` lookup).
- On a cold start the poll can publish a PR and the scan can retry it in the
  same cycle — harmless (the controller dedups), but it counts as a retry.
- If `PublishCreate` keeps returning `false` (e.g. Kafka down), attempts never
  reach the budget, so `retries_exhausted` never flips — watch
  `github_pr_watcher_prs_total{command="error"}` for that case.

Known limitation: `UpdatedAt` moves on comments and label changes too — including
the watcher's own trivial-PR label — which restarts the wait and can delay a
report. Cost: about two core API calls per qualifying open PR (PR details +
reviews), more for raw-oversized PRs that need the `.reviewignore` lookup, all on
the shared App token.

## HTTP Endpoints

| Path | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Liveness probe (always returns 200 OK) |
| `/readiness` | GET | Readiness probe (always returns 200 OK) |
| `/metrics` | GET | Prometheus metrics |
| `/check` | POST | Run a poll cycle in the background; returns 200 immediately |
| `/trigger` | POST | Fire a single-PR review by URL (`?url=<pr_url>`); reuses the filter chain and trust evaluation |

### Single-PR Trigger Known Limit

If a vault task already exists for the same `(PR, SHA)`, the controller's `create-if-not-exists` is idempotent and no fresh agent Job spawns. To force a re-run in that case, call `/trigger?url=<pr_url>&force=true` (the forced run salts the task title with a retry token, so it lands on a fresh record) OR push a new commit so the SHA changes.

### Un-parking a Parked Record

A parked record (oversized or untrusted author) carries no PR URL in its body, so the reviewer fails it if it is un-parked by editing its frontmatter. Use the lever for the park reason instead:

- **Oversized PR** — call `/trigger?url=<pr_url>&force=true`. Force skips the oversized park and composes a fresh, reviewable record.
- **Untrusted author** — add the login to `TRUSTED_AUTHORS` and restart the watcher: the list is read at startup, so the change takes effect only after a restart. Then push a new commit (new SHA), or call `/trigger?url=<pr_url>&force=true` — the forced run salts the title with a retry token, so it lands on a fresh record. A plain `/trigger` for the same SHA does not: it composes the same title as the parked record, and the controller's `create-if-not-exists` is idempotent (see above). Force does not skip the trust check.

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

## Mechanical override for trivial bot bumps

A pull request can be approved without an LLM review when the watcher can prove it is
a trivial bot dependency bump. A PR qualifies when all of the following hold:

- its author is listed in `MECHANICAL_OVERRIDE_AUTHORS` (exact, case-sensitive match);
- the author is trusted (`TRUSTED_AUTHORS`);
- the repo opts in through **both** `.maintainer.yaml` flags: `autoMerge.trivial` and
  `prReviewer.autoApprove`;
- every changed file is mechanical per `ClassifyTrivial` (no hand-written code);
- the PR is not a draft and carries no override label.

In the poll that qualifies, the watcher applies the auto-merge label, arms
GitHub-native auto-merge, and publishes one `pr-override` task with
`override_reason: trivial-bot-bump` whose body states that no LLM review ran. No
review task is published for that head SHA. The watcher never merges: GitHub
performs the merge once the required checks are green.

Dedupe is per (PR, head SHA) and shared with the label override — both override
kinds use the same override task-id, so a SHA is approved at most once.

**Release ordering.** `bborbe/github-pr-review-agent` must understand
`override_reason: trivial-bot-bump` before this watcher is deployed; otherwise the
agent falls back to the label wording. The `go.sum` classifier change must ship in
the same watcher release — without it `ClassifyTrivial` rejects every real bump and
the feature stays dark.

**Kill switch.** `TRIVIAL_AUTO_MERGE` governs only the labelling path. Mechanical
approvals are switched off by emptying `MECHANICAL_OVERRIDE_AUTHORS` — an incident
that must stop unattended approvals clears that list, not the flag.

**Failure fall-throughs.** A config or file-listing error means "review this poll".
A failed label write also means "review this poll" — an approve never lands without
its arming. A failed arming after a successful label write still publishes the
approve; the label is now present, so the next poll's auto-merge path retries the
arming.

An empty `MECHANICAL_OVERRIDE_AUTHORS` list is the off switch; there is no separate
enable flag.

## Auto-merge arming window

Arming is not the same as merging, and it cannot be done at any time.

GitHub exposes auto-merge only through the GraphQL `enablePullRequestAutoMerge`
mutation. There is no REST endpoint — `PUT /repos/{owner}/{repo}/pulls/{number}/auto-merge`
returns 404 Not Found, which is why go-github ships no typed wrapper for it.

The mutation rejects a pull request that is **already mergeable**:

```
UNPROCESSABLE: Pull request Pull request is in clean status
```

Auto-merge is a promise to merge once the remaining requirements are met, so
there must be a remaining requirement. A pull request that has already gone
green and collected its approvals cannot be armed — at that point it can only
be merged outright.

This matters because the watcher's search is cursor-based: a pull request is
processed on the poll following its last update, so arming gets roughly one
attempt per update. In the normal agent flow that attempt lands within one poll
interval of PR creation, while CI is still running and no review exists yet, so
the pull request is blocked and arming succeeds. A pull request that becomes
mergeable faster than the poll interval will not be armed, and the watcher logs
the refusal at error level rather than failing silently.
