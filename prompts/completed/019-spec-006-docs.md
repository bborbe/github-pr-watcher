---
status: completed
spec: [006-park-age-metric]
summary: Documented the github_pr_watcher_parked_prs gauge and PARK_LEDGER_PATH env var in README.md with seed/staleness/single-replica caveats, pointed the un-parking section at the gauge, and added a docs bullet to CHANGELOG.md; make precommit passed.
execution_id: 3546659f-32d3-562a-a7c7-870cb565441e-exec-019-spec-006-docs
dark-factory-version: dev
created: "2026-11-22T15:47:00Z"
queued: "2026-11-22T14:59:40Z"
started: "2026-11-22T15:08:45Z"
completed: "2026-11-22T15:10:36Z"
branch: dark-factory/park-age-metric
---

<summary>
- The README's `## Metrics` table gains a row for the new parked-PR gauge, described as oversized-parked PRs.
- The row states the seed caveat (seeded from updated-at, so it can lag the true park), the one-scan staleness bound (replaced per complete scan, failed scan leaves previous values), and the single-replica caveat.
- The environment-variable table gains a `PARK_LEDGER_PATH` row with its default and the note that the watcher is the only writer.
- The "Un-parking a Parked Record" section gains one sentence pointing at the gauge so an operator can bound and confirm a park.
- No code changes; the two greps in the spec's acceptance criterion pass.
</summary>

<objective>
Document the `github_pr_watcher_parked_prs` gauge and the `PARK_LEDGER_PATH` env var in `README.md`, including the seed, staleness, and single-replica caveats the spec's Constraints require, and point the existing "Un-parking a Parked Record" section at the metric.
</objective>

<context>
Read `README.md` in full. The three places to edit:

- `## Environment Variables` table (rows end around `UNREVIEWED_RETRY_MAX`; `MAX_CHANGED_FILES` is directly above the reviewer-bot rows). Follow the existing column shape: `| Variable | Required | Default | Description |`.
- `## Metrics` table (the `github_pr_watcher_unreviewed_*` and `github_api_*` rows are the template). Follow `| Metric | Type | Labels | Meaning |`.
- `### Un-parking a Parked Record` (the subsection with the oversized-PR and untrusted-author bullets).

Read `pkg/metrics.go` for the exact metric name, type, labels, and help string, and `main.go` for the `PARK_LEDGER_PATH` env var and its default, so the docs match the code. Read `pkg/park_ledger.go` for the ledger file shape and write semantics.

Coding-guide references (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/documentation-guide.md`
</context>

<requirements>
1. **Environment Variables row.** Add a `PARK_LEDGER_PATH` row to the `## Environment Variables` table, near the other unreviewed/oversized rows:
   ```markdown
   | `PARK_LEDGER_PATH` | no | `/data/park-ledger.json` | Path to the JSON ledger the watcher reads at the start of each scan and rewrites atomically at its end, recording when each parked oversized PR started and at which head SHA. Written `0600`; mounted on a PersistentVolumeClaim at `/data` so park ages survive a restart. The watcher is the only writer — do not edit it by hand |
   ```

2. **Metrics row.** Add a `github_pr_watcher_parked_prs` row to the `## Metrics` table, next to the `github_pr_watcher_unreviewed_*` rows:
   ```markdown
   | `github_pr_watcher_parked_prs` | gauge | `repo` (`owner/repo`), `pr` (number) | Seconds each open, trusted-author, **oversized-parked** PR has been continuously parked at `human_review`. Untrusted-author parks are dropped by the trust check before the park decision and are not covered. The value is seeded from the PR's `updatedAt` on the first scan that observes it parked — so it can lag the true park by up to `UNREVIEWED_THRESHOLD` — then held in `PARK_LEDGER_PATH` until the head SHA changes: a comment does not reset it, a push does. A complete scan replaces the whole set, so a park that ends (closed, drafted, or pushed under either limit) disappears within one scan; a failed scan leaves the previous values. Single replica only — two replicas keep independent ledgers and may disagree |
   ```

3. **Un-parking section.** Add one sentence to `### Un-parking a Parked Record` (after its opening sentence, before the two reason bullets) pointing at the metric:
   ```markdown
   While a PR is parked, `github_pr_watcher_parked_prs{repo="<owner/repo>",pr="<n>"}` reports how many seconds it has been parked — use it to bound the wait and to confirm the un-park took effect.
   ```

4. Do not restate the full help string verbatim in the table; the row above already carries the seed, staleness, and single-replica caveats the spec requires. Keep the existing rows and sections unchanged.
</requirements>

<constraints>
- The metric name `github_pr_watcher_parked_prs`, its labels `repo` and `pr`, and the env var `PARK_LEDGER_PATH` (default `/data/park-ledger.json`) are frozen; copy them exactly.
- Describe the gauge as **oversized-parked** PRs and state explicitly that untrusted-author parks are not covered.
- Include the seed caveat, the one-scan staleness bound, and the single-replica caveat.
- Do NOT modify code. Do NOT commit — dark-factory handles git.
- Rung-3 post-deploy checks are operator-side and are NOT verified by this prompt.
</constraints>

<verification>
- `grep -c 'github_pr_watcher_parked_prs' README.md` — at least 1.
- `grep -c 'PARK_LEDGER_PATH' README.md` — at least 1.
- `grep -n 'oversized-parked\|Untrusted-author parks\|Single replica' README.md` — the caveats are present.
- `make precommit` — must pass (no code changed, but the tree must stay green).
</verification>
