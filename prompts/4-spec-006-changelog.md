---
status: draft
spec: [006-park-age-metric]
created: "2026-11-22T15:48:00Z"
branch: dark-factory/park-age-metric
---

<summary>
- Adds an `## Unreleased` section to the CHANGELOG with one feature bullet for the parked-PR age metric.
- The bullet names the gauge, the ledger file, the env var, and the seed/comment/push semantics in one line.
- Follows the repo's changelog convention: `## Unreleased` sits directly above the highest version heading, with a `feat:` prefix.
- No code changes.
- The entry is release-notes-ready: the next release picks up a minor bump from the `feat:` prefix.
</summary>

<objective>
Record the new parked-PR age metric in `CHANGELOG.md` under a new `## Unreleased` section, using the repo's conventional `feat:` prefix, so the next release bumps the minor version.
</objective>

<context>
Read `CHANGELOG.md`. The file currently has the standard preamble (title, "All notable changes…", the Keep a Changelog line, the SemVer line) followed directly by `## v0.29.2` — there is no `## Unreleased` section yet.

Read `pkg/metrics.go` and `pkg/park_ledger.go` for the exact metric name, env var, and ledger semantics so the bullet matches the code.

Coding-guide references (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. Insert an `## Unreleased` section immediately AFTER the preamble's last line (`and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).`) and directly ABOVE `## v0.29.2`. Do not move, delete, or insert anything inside the preamble block (everything from `# Changelog` through the SemVer line).
2. Add exactly one bullet under it, starting with `feat:`:
   ```markdown
   ## Unreleased

   - feat: publish `github_pr_watcher_parked_prs{repo,pr}` — the seconds an oversized PR has been continuously parked at `human_review` — on `/metrics`. Each complete scan records the park's start in a JSON ledger at `PARK_LEDGER_PATH` (default `/data/park-ledger.json`), seeded from the PR's `updatedAt` and held until the head SHA changes, so the age survives a restart and a comment does not reset it while a push does; a complete scan replaces the whole set, so a park that ends disappears within one scan. Untrusted-author parks are not covered. Single replica only.
   ```
3. Keep the flat list format (no `### Added` / `### Fixed` subheadings). Do not add a version number or date.
</requirements>

<constraints>
- Preamble frozen: nothing may be inserted above or inside the header block; `## Unreleased` goes after the last preamble line and before `## v0.29.2`.
- The bullet must start with a recognised conventional prefix (`feat:`).
- Do NOT modify code. Do NOT commit — dark-factory handles git.
</constraints>

<verification>
- `grep -n '## Unreleased' CHANGELOG.md` — exactly one, above `## v0.29.2`.
- `grep -n 'github_pr_watcher_parked_prs' CHANGELOG.md` — the bullet is present.
- `make precommit` — must pass (no code changed, but the tree must stay green).
</verification>
