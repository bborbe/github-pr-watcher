---
status: draft
---

## Summary

- An oversized PR is parked at `human_review` with no reviewer pod, and the park body explains why and gives the force-review lever — but nothing measures how long the park has lasted.
- The watcher already computes, on its full-open-PR scan, exactly which open PRs are parked. It drops that answer on the floor.
- This change makes the watcher remember when each PR parked, survive its own restarts, and publish each park's age on `/metrics`.
- A 24 h alert can then fire on a real number instead of a counter that only records that a park happened.

## Problem

The only wait-measuring signal the watcher exports is `github_pr_watcher_unreviewed_prs`, and its scanner returns early on parked PRs (`pkg/unreviewed_scanner.go:652`, `if s.parked(...)`), so parked oversized PRs are excluded from it by construction. The park counter `github_pr_watcher_prs_total{command="parked"}` records park *events*, not durations. No rule in `bborbe/nuke` `alerts/` references either metric.

The consequence is that a parked PR waits indefinitely and silently. `bborbe/attention-controller#258` has been parked since 2026-11-18 with no bound and no signal, and `bborbe/trading-lib#666` since 2026-11-22. The park record explains the park; nothing bounds it.

**Where a park record lives.** One markdown file per (repo, PR, head SHA) under `<vault>/tasks/`, named `PR Review github - <owner>-<repo> - <pr> - <sha8> - <slug>.md`; its body opens `## Oversized PR — parked for human review` and states the counted additions and changed files against their limits; its frontmatter carries `phase: human_review`, `status: next`, and `ref: <head SHA>`. `<vault>` is `~/Documents/Obsidian/private-agent` in prod. This is the artifact AC 2 compares against.

## Goal

Every currently-parked oversized PR is observable on the watcher's `/metrics` as a series carrying its age in seconds, the age is continuous across a watcher restart, and the series disappears when the PR stops being parked. An alert rule can then be written against that series without any further watcher change.

## Non-goals

- The park decision itself — `oversized`, the thresholds, and the emitted park body are unchanged.
- The nuke chart: the PVC, the alert rule, the `promtool` fixture, and the image pin land in a separate `bborbe/nuke` PR.
- Recovering the exact park time for parks that predate the ledger — see `## Desired Behavior` item 3 for the documented seed.
- Reworking the retry guard, the redrive ledger, or the unreviewed retry pass.
- Making the park disappear, or routing a verdict without a reviewer pod.
- Publishing from the incremental poll (`Poll`): its `fetchAllPRs(ctx, cursorState.LastUpdatedAt)` window only covers PRs updated since the cursor, so a parked PR that stops being updated leaves that window. The publisher is the full-open-PR scan, not the poll.
- Operator editing of the ledger file as a supported workflow. The file is written and read only by the watcher, and `PARK_LEDGER_PATH` exists so tests can point at a temp file — no operator override of the production path is expected.

## Assumptions

- The watcher runs as a single replica (the prod StatefulSet is `replicas: 1`).
- A PR's `updatedAt` is the clock the scanner already treats as reviewable-since (`pkg/unreviewed_scanner.go:619`): it moves on open, on a new push, and on draft→ready, each of which restarts the wait.
- The nuke PR that mounts a PersistentVolumeClaim at `/data` is merged and deployed before the Rung-3 ACs are walked — see `## Verification ladder precondition`.

## Acceptance Criteria

- [ ] **Post-Deploy (Rung-3):** both currently-parked PRs carry a series with a positive age — `github_pr_watcher_parked_prs{repo="bborbe/trading-lib",pr="666"}` and `github_pr_watcher_parked_prs{repo="bborbe/attention-controller",pr="258"}` are present with values > 0. Evidence: both lookups against the running pod's `/metrics`. If either has un-parked by the time this is walked, the next criterion's fresh-park path is the fallback.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** for a PR that parks after this change is deployed and receives no comment afterwards, the reported age is within ±15 min of the interval between its record's commit time and the read time. Read only once the series exists — at least `UNREVIEWED_THRESHOLD` plus one `POLL_INTERVAL` after the park. Evidence: `git -C ~/Documents/Obsidian/private-agent log -1 --format=%cI -- '<record>'` versus the metric value at read time.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** the age survives a watcher restart — after `kubectlnukeprod -n agent rollout restart statefulset github-pr-watcher`, the value read is at least the pre-restart value plus the restart duration, and is not reset toward zero. Evidence: two reads, one either side of the restart.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] **Post-Deploy (Rung-3):** a park that ends removes its series within one complete scan — the PR is closed, converted to draft, or pushed to a head SHA that no longer exceeds either limit. Evidence: the series is present before the change and absent after `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds` has advanced past it.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] The ledger, not the clock, decides the age — three unit tests, each asserting the published value:
  1. same head SHA, the PR's `updatedAt` set to **now**, ledger start 3600 s ago → `3600 ± 60` — proves the ledger, not `updatedAt`, is the source;
  2. same head SHA, `updatedAt` moved forward as a comment would, ledger start 3600 s ago → `3600 ± 60` — proves a comment does not reset a park;
  3. **different** head SHA whose `updatedAt` is now, ledger start 3600 s ago → `0 ± 60` — proves a push restarts the park;
  4. **no ledger entry at all**, `updatedAt` 3600 s ago → `3600 ± 60` — proves a new entry is seeded from `updatedAt`, not from the observation time.

  Evidence: all four pass under `go test ./pkg/...`.
- [ ] **Post-Deploy (Rung-3):** the scan writes the ledger — after a complete scan, `PARK_LEDGER_PATH` contains an entry keyed `<owner>/<repo>#<pr>` carrying both a `start` and a `sha`, and it is **pruned** — once a park ends and one complete scan runs, that PR's key is gone. Evidence: `kubectlnukeprod -n agent exec github-pr-watcher-0 -- cat /data/park-ledger.json` shows the entry with a `sha` equal to that PR's current head SHA (`gh -R <owner>/<repo> pr view <n> --json headRefOid`), and after the park ends `grep -c '<owner>/<repo>#<pr>'` against that file returns 0.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`
- [ ] No park behaviour changes and the tree is clean: `git diff origin/master -U0 -- pkg/watcher.go pkg/filter/ pkg/unreviewed_scanner.go | grep -cE '^[+-].*(buildHumanReviewFrontmatter|buildParkedBody|func oversized|MaxChangedFiles|MaxAdditions)'` returns 0, the existing park tests pass unmodified, and `make precommit` exits 0. Evidence: that count, `make test` green, and the exit code.
- [ ] The new metric and its env var are documented: `grep -c 'github_pr_watcher_parked_prs' README.md` ≥ 1 and `grep -c 'PARK_LEDGER_PATH' README.md` ≥ 1. Evidence: both counts.

## Verification

### Verification ladder precondition

The ledger lives at `/data/park-ledger.json`. The runtime image has no `/data` (the final `FROM alpine` stage of `Dockerfile` declares no `WORKDIR` and no `VOLUME`), so until the `bborbe/nuke` PR mounts a **PersistentVolumeClaim** there — an `emptyDir` would be wiped by the same `rollout restart` AC 3 tests — every ledger write fails and every Rung-3 AC fails for a reason unrelated to this change. That PR belongs to a sibling task outside this spec, is a gate rather than a follow-up, and **no prompt generated from this spec touches `bborbe/nuke`** — this project holds write credentials for `bborbe/github-pr-watcher` only, so a prompt that tried to clone or push `bborbe/nuke` would fail on credentials and lose the commit.

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — format, lint, tests, security checks
- `make test` — unit + integration suite
- `grep -n 'github_pr_watcher_parked_prs' pkg/metrics.go` — the metric name is registered
- `go test ./pkg/...` — the Ginkgo suite (the suite entry point is `TestSuite`, so `-run` filtering would match nothing and exit 0 falsely)

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}'` — deployed tag is the released one
- `kubectlnukeprod -n agent get --raw '/api/v1/namespaces/agent/pods/github-pr-watcher-0:9090/proxy/metrics' | grep github_pr_watcher_parked_prs` — the series is live
- `kubectlnukeprod -n agent exec github-pr-watcher-0 -- cat /data/park-ledger.json` — the ledger exists and shows its shape

## Desired Behavior

1. **One series per parked PR.** Each **complete** full-open-PR scan publishes a gauge vec `github_pr_watcher_parked_prs` labelled `repo` and `pr`, whose value is the number of seconds the PR has been continuously parked.
2. **The set is replaced per complete scan.** Publishing follows the existing `SetUnreviewedPRs` contract: a complete scan replaces the whole set, and a failed or skipped scan leaves the previous values in place. A PR that stops being parked disappears on the next complete scan.
3. **A park's start is seeded from `updatedAt`, then frozen.** The first scan that observes a PR as parked records its `updatedAt` as the start. Seeding from that clock rather than from first observation matters because the scanner evaluates `parked()` only after its unreviewed-threshold gate (`pkg/unreviewed_scanner.go:619`), so first observation lags the park by up to `UNREVIEWED_THRESHOLD`. A comment moves `updatedAt` forward; the ledger entry written on the first scan is authoritative until its head SHA changes.
4. **The ledger outlives the process.** Park starts are persisted to `/data/park-ledger.json`, read at the start of each complete scan and rewritten at its end, so a restart does not reset any age.
5. **A new head SHA restarts the age; a comment does not.** The ledger entry stores the head SHA alongside the start. When the scan sees a different SHA for the same PR, it replaces the start; when it sees the same SHA, it keeps it, so a comment cannot reset a park.
6. **The ledger is bounded.** Entries for PRs no longer parked are dropped each complete scan, so the file tracks only the live parked set.

## Constraints

- Go, following the repo's existing patterns: Ginkgo/Gomega tests in `pkg_test`, `github.com/bborbe/errors` for wrapping, `libtime.DateTime` where the existing code uses it, Counterfeiter mocks under `mocks/`.
- **Frozen interfaces** — the nuke alert rule, the README row and the verification commands depend on them:
  - metric name `github_pr_watcher_parked_prs`, labels `repo` and `pr`, value in seconds.
  - env var `PARK_LEDGER_PATH`, default `/data/park-ledger.json`; the nuke chart mounts a PersistentVolumeClaim at `/data`.
  - ledger file shape:
    ```json
    {"entries": {"<owner>/<repo>#<pr>": {"start": "<RFC3339>", "sha": "<head sha>"}}}
    ```
- No new external dependency for storage. A file on the mounted path is sufficient.
- Writes are atomic: temp file in the same directory, then `rename`. A torn write leaves the temp file, never a partial ledger.
- The scan must not block on ledger I/O: both the read at scan start and the write at scan end carry a 2 s timeout, and a slow or failed one must not delay the next scan or the unreviewed publish.
- The ledger file is written `0600`.
- Existing metric names and help strings are unchanged.
- README gains a `## Metrics` table row for the new gauge — described as **oversized-parked** PRs (untrusted-author parks are dropped by the trust check before `parked()` and are not covered) — including the seed caveat, the one-scan staleness bound, and the single-replica caveat; plus a `PARK_LEDGER_PATH` row in the env-var table and one sentence in § "Un-parking a Parked Record" pointing at the metric.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| Ledger file missing at startup or at scan | Continue empty and seed on the scan; log at INFO | None — the scan repopulates |
| Ledger file corrupt / unparseable | Log an error naming the path, keep the in-memory entries, continue scanning | `kubectlnukeprod -n agent exec github-pr-watcher-0 -- cat /data/park-ledger.json`; the next successful scan rewrites it |
| Torn write (killed between temp write and rename) | The temp file is ignored on load; the previous ledger stands | None — the next scan rewrites |
| Ledger write fails (disk full, read-only mount) | Log an error, keep the in-memory ledger authoritative for the process lifetime, continue scanning | Ages survive within the process but re-seed on restart until the mount is fixed |
| PVC lost or remounted empty | Every age re-seeds from `updatedAt`; no age is invented | Operator sees ages drop on the next scan; the seed is the documented fallback |
| Scan aborts (GitHub error) | Previous gauge values stay in place, so a stale park age persists | Operator sees `github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds` stop advancing; the series clears on the next successful scan |
| Watcher restarts mid-park | Reload the ledger; age continues from the recorded start | None — this is the case the ledger exists for |
| A PR is pushed to a new head SHA while parked | The ledger entry is replaced and the age restarts from the new `updatedAt` | None — a new SHA is a new park |
| Two replicas run simultaneously | Each keeps its own ledger, last writer wins, ages may disagree between pods | Single-replica StatefulSet; documented in the README row, not defended against |
| Clock skew on the host | Age is a wall-clock delta; skew shifts every age equally | Node time sync; a negative age is clamped to 0 |

## Security / Abuse

No new input surface: the ledger path is operator-supplied config, and its contents are derived only from PRs the watcher already polls. The file is written `0600`. No user-controlled string reaches a shell.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Park ledger: entry type with start + sha, seed, prune, read-each-scan, atomic write, corrupt-file tolerance, the three unit tests | 3, 4, 5, 6 | 5, 7 | — |
| 2 | Scan integration + metric: `SetParkedPRs` on the interface, collect the parked set where `qualify` drops it, help text, registry test | 1, 2 | 1, 2, 3, 4, 6, 7 | prompt 1 |
| 3 | Docs: README `## Metrics` row, env-var row, park-section sentence | — | 8 | prompt 2 |
| 4 | CHANGELOG `## Unreleased` bullet | — | — | prompts 1-3 |

AC 7 (`no park behaviour change` + `make precommit`) applies to every prompt and is not owned by any one row.

## Do-Nothing Option

The park stays unbounded and invisible. The operator finds a parked PR only by opening its record, and the 24 h alert the task exists to enable cannot be written, because there is no series to alert on. The cost is the current one: `attention-controller#258` has waited four days with nothing surfacing it.
