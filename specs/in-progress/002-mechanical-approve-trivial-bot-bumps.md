---
status: prompted
approved: "2026-10-24T08:19:36Z"
generating: "2026-10-24T08:25:48Z"
prompted: "2026-10-24T08:29:16Z"
branch: dark-factory/mechanical-approve-trivial-bot-bumps
---

## Summary

- A trivial bot dependency bump gets a mechanical approve instead of an LLM review.
- The watcher proves triviality itself, with the classifier and the repo's own opt-in flags.
- The approve rides the existing override task, so no new task type and no new agent path is needed.
- The watcher also applies the auto-merge label and arms native auto-merge for that PR, so the merge still happens without a human.
- Every other PR keeps today's behavior exactly.

## Problem

A dependency-bump PR opened by `ben-s-go-updater[bot]` changes nothing but version strings and their hashes, yet it waits for a full LLM review before the ruleset's required approving review exists — pure latency and pure cost on a change the watcher can prove mechanical, and while the reviewer fleet is backed up these PRs sit for hours against a goal that promises a verdict in minutes.

## Goal

A trivial bot dependency bump on an opted-in repo merges without an LLM review: the watcher applies the auto-merge label, arms native auto-merge, and publishes one override task carrying `override_reason: trivial-bot-bump`; the review agent posts a mechanical approve naming the rule; GitHub merges on green. No review task is created for that head SHA.

## Non-goals

- No direct merge and no new merge path: the watcher never merges. It arms, exactly as it already does for these PRs; GitHub performs the merge.
- No change to the trivial classifier — `go.sum` support ships in its own spec.
- The existing `maybeLabelTrivial` step is unchanged; the mechanical path performs its own label write rather than relying on it.
- No LLM-skipping variant implemented in this repo: the agent-side branch keyed on `override_reason` ships in `bborbe/github-pr-review-agent` PR #53, and this spec only publishes the field. The queue slot and the agent round-trip remain; the cost this spec removes is the LLM inference.
- No new task type, no new agent phase, and no change to the review agent's code (that side ships separately, see Assumptions).
- No widening to human authors, and no LLM-based classification.
- No mechanical approve for a repo that has not opted in through both `.maintainer.yaml` flags.

## Assumptions

- `maintainerconfig.MaintainerConfig` in `github.com/bborbe/maintainer` v0.50.12 exposes `PrReviewer.AutoApprove` and `AutoMerge.Trivial` (fields verified on the pinned version with `go doc`).
- `ben-s-go-updater[bot]` and `ben-s-go-updater-dev[bot]` are both on `TRUSTED_AUTHORS` in dev (`values-dev.yaml` lists both), and `ben-s-go-updater[bot]` is on it in prod. The mechanical gate re-checks trust rather than assuming it, so a stage that drops a login falls back to a review instead of an unarmed approve.
- The `bborbe/math` canary has opted in: its `.maintainer.yaml` carries `prReviewer.autoApprove: true` and `autoMerge.trivial: true` (read 2026-10-24), so it satisfies gates 4 and 5 without a change.
- Every `bborbe` repo's `master` ruleset requires one approving review plus status checks; the mechanical approve satisfies the review half and nothing bypasses the checks.
- The review agent understands `override_reason: trivial-bot-bump` from `bborbe/github-pr-review-agent` PR #53 onward; before that release the field is ignored and the label body is posted. That PR must be merged, released and deployed before the watcher, per stage.

## Acceptance Criteria

Every entry name below belongs to the Ginkgo container `Describe("Mechanical override for trivial bot bumps")`, so `go test ./pkg/ -v -ginkgo.focus='Mechanical override'` selects exactly this set. Each AC is satisfied when that focused run lists the named entries and reports 0 failures.

- [ ] A qualifying PR publishes one command whose frontmatter `task_type` is `pr-override`, `override_reason` is `trivial-bot-bump`, `ref` equals the head SHA, and no `pr-review` command is published for that SHA. Evidence: the entry `qualifying bump -> override task with rule body`.
- [ ] That override body contains `trivial-bot-bump` and `No LLM review ran`, and does not contain `override-review`. Evidence: the same entry, asserting the three body clauses.
- [ ] In that poll the auto-merge label is present on the PR afterwards and `EnableAutoMerge` is called once, so the merge is armed without depending on `TRIVIAL_AUTO_MERGE`. Evidence: the entry `qualifying bump -> label present and auto-merge armed`.
- [ ] **Negative:** the same author and repo config with files that fail `ClassifyTrivial` produce a `pr-review` command and no `pr-override` command. Evidence: the entry `non-trivial files -> review only`.
- [ ] **Negative:** each of these produces one `pr-review` publication and no override — author not on `MECHANICAL_OVERRIDE_AUTHORS`; author not trusted; author whose login differs only in case from a list entry; `autoMerge.trivial` false; `prReviewer.autoApprove` false. Evidence: five entries, `author not on mechanical list -> review only`, `author not trusted -> review only`, `author case differs -> review only`, `autoMerge.trivial false -> review only`, `prReviewer.autoApprove false -> review only`.
- [ ] **Negative:** a draft PR produces zero publications of either kind, because the draft filter short-circuits before the gate. Evidence: the entry `draft -> no publication`, asserting zero `PublishCreate` and zero `PublishOverride` calls.
- [ ] **Negative:** a PR carrying the override label publishes a `pr-override` command with no `override_reason` key and the existing label body, whether or not it would otherwise qualify. The label path's task body contains `A trusted author applied the override label.`; the mechanical body must not carry that sentence, and the agent's review body must not name the `override-review` label. Evidence: the entry `override label present -> label override only`, asserting the absent `override_reason` key and the present sentence.
- [ ] At most one override is published per (PR, head SHA) across repeated polls, and a new head SHA produces a new one. Evidence: the entry `dedupe per head SHA` polling twice with an unchanged SHA (one publication) then once with a bumped SHA (a second publication).
- [ ] A GitHub API error publishes no override: a failing config read and a failing file listing each still publish the `pr-review` command, while a failing trust check publishes nothing at all (today's behavior, unchanged). Evidence: three entries — `config read fails -> review only`, `file listing fails -> review only`, `trust check fails -> no publication`.
- [ ] The emission is observable: the metrics fake records `IncPRPublished` with the argument `override_mechanical` exactly once in the emitting poll, and the label is pre-created at zero in the metrics init list. Evidence: the entry `emitting poll records override_mechanical`, plus `grep -n 'override_mechanical' pkg/metrics.go` returns ≥1 line.
- [ ] **Post-Deploy (Rung-2):** a real bump PR on the `bborbe/math` canary reaches `MERGED` after the mechanical approve, with an `APPROVED` review from `ben-s-pull-request-reviewer[bot]` whose body names the rule, a `mechanical override` log line naming its head SHA, and a `PR Override github - bborbe-math - <n> - *` record in the agent tasks vault. The PR number is the first bump PR `ben-s-go-updater-dev[bot]` opens on `bborbe/math` after the dev deploy, and is recorded in the implementing PR's description; the canary already carries both `.maintainer.yaml` flags. Evidence: `gh -R bborbe/math pr view <n> --json state,reviews` reports `MERGED` with that approve; `kubectlnukedev -n dev logs deploy/github-pr-watcher --since=30m | grep 'mechanical override'` returns ≥1 line; `git -C ~/Documents/Obsidian/private-agent log --diff-filter=A -m --name-only -- ':(top)tasks/PR Override github - bborbe-math - <n> - *'` returns a record.
  - `deploy_check:` `kubectlnukedev -n dev get deploy/github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MECHANICAL_OVERRIDE_AUTHORS")].value}' | grep -q . && kubectlnukedev -n dev get deploy/github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`, evaluated in the host repo checked out at the released master commit the deploy consumed — not from a feature worktree, where it would resolve to an older tag

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — fmt, generate, test, lint, vet, vuln, license
- `go test ./pkg/ -v -ginkgo.focus='Mechanical override'` — the new specs pass
- `grep -n 'MECHANICAL_OVERRIDE_AUTHORS' main.go` — the configuration value is wired
- `grep -n 'trivial-bot-bump' pkg/` — the frontmatter value is emitted from the watcher
- `grep -n 'override_mechanical' pkg/` — the metric label exists at both the call site and the metrics init list

### Operator-executable (runs on the host after PR merge, spec verification ladder)

Run these in order, per stage. The agent step is first because a watcher that emits `override_reason` before the agent understands it posts the label body.

1. Confirm `bborbe/github-pr-review-agent` PR #53 is merged and its release tag is deployed in the stage's agent workload. Confirm the watcher release being deployed also carries the `go.sum` classifier change (`go-sum-trivial-classification.md`) — same binary, so one release covers both, but the classifier must not be left out of it.
2. Set `MECHANICAL_OVERRIDE_AUTHORS: ben-s-go-updater-dev[bot]` in `github-pr-reviewer/values-dev.yaml` of the `nuke-dev` repo (the chart directory is `github-pr-reviewer/`; the deployment it renders for this service is `github-pr-watcher`), bump the watcher tag to the release carrying this change, and `kubectlnukedev -n dev rollout status deploy/github-pr-watcher`. An empty list is off, so this values change is what activates the feature; record it in the PR description so it is not deployed dark.
3. The same values change and deploy in `nuke-prod` with `MECHANICAL_OVERRIDE_AUTHORS: ben-s-go-updater[bot]`; the prod `make apply` is the operator's own keystroke.
4. `VERSION=vX.Y.Z make buca` for each release the steps above consume.

## Desired Behavior

1. A PR qualifies only when all six gates hold: the author is on `MECHANICAL_OVERRIDE_AUTHORS`; the author is trusted; the draft filter has not already dropped the PR (it runs before the gate, unchanged); `autoMerge.trivial` is true for the repo; `prReviewer.autoApprove` is true for the repo; the changed files pass `ClassifyTrivial`.
2. A qualifying PR gets the auto-merge label if it lacks it, is armed, and gets exactly one `pr-override` task with `override_reason: trivial-bot-bump`, a body naming the rule, and `ref` set to the head SHA — and gets no review task for that SHA.
3. A non-qualifying PR is untouched: it follows the existing path. A PR carrying the override label keeps the label override, which takes precedence over the mechanical gate.
4. A GitHub API error on the config or file read means "does not qualify" — the PR falls back to the normal review. A trust-check error keeps today's behavior, which is no publication either way.
5. Dedupe is per (PR, head SHA) and shared with the existing override dedupe: a re-poll with the same SHA emits nothing, a new SHA emits once.
6. The emission is visible: one log line naming the PR, the head SHA and the rule, plus the `override_mechanical` counter label.

## Constraints

- **Release ordering.** Deploy the review agent before the watcher, per stage. An agent that predates PR #53 ignores `override_reason` and posts the label body, which wrongly claims a trusted author applied the label.
- **Classifier ordering.** The `go.sum` classifier spec (`go-sum-trivial-classification.md`) ships before this one, per stage. Without it `ClassifyTrivial` returns false for every real bump, so gate 6 never passes and the feature stays dark even with the author list set.
- **The mechanical override carries its own body.** It reuses the override task type but not the label wording: the body names the rule and states that no LLM review ran.
- **Label precedence.** The override label is an explicit human action and wins over the mechanical gate; a PR carrying it is never routed through the mechanical path.
- **Three author lists, three meanings.** `BOT_ALLOWLIST` means "handled elsewhere, skip entirely"; `TRUSTED_AUTHORS` means "may receive a review and may have auto-merge armed"; `MECHANICAL_OVERRIDE_AUTHORS` means "may be approved without a review". A shared knob would conflate skipping with approving. All three match by exact case-sensitive login.
- **`BOT_ALLOWLIST` wins over everything.** The bot-author filter runs in `taskCreationFilter` before the gate, so adding a mechanical author there makes `processPR` return early and silently disables the feature. Do not add a mechanical author to `BOT_ALLOWLIST`.
- **`prReviewer.autoApprove` is the operator's standing consent** to automatic approvals on that repo; it is the third key of a three-key opt-in (`autoMerge.trivial` + `prReviewer.autoApprove` + the author list).
- **Independent of `TRIVIAL_AUTO_MERGE`.** The mechanical gate reads the repo config and the file list itself, and applies the auto-merge label itself, so arming never depends on the labelling step being enabled. The cost is at most two extra reads and one label write per qualifying PR per poll; no shared-cache refactor is required.
- **Empty list is the off switch.** An empty `MECHANICAL_OVERRIDE_AUTHORS` means nobody qualifies; no separate enable flag is added.
- **The classifier stays conservative.** A false positive ships an unreviewed behavior change; a false negative costs one review.
- **The new counter label is registered at zero.** `pkg/metrics.go` pre-creates every `command` label so alerts see the series before the first event; `override_mechanical` joins that list.
- **Never merge.** No merge API call is added; native auto-merge on green remains the only merge path.
- **Existing behavior must not regress:** the label-override path, the review path, the auto-merge labelling and arming for PRs that do not qualify, and supersession are unchanged.
- **Tests:** Ginkgo v2 / Gomega, Counterfeiter mocks from `mocks/`, external test packages. No LLM step is involved.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `GetMaintainerConfig` or `ListPRFiles` fails | No override; the review command is still published for that SHA; the error is logged and the counter increments with the error label | Confirm the next poll's log line `mechanical override pr=<owner>/<repo>#<n>` and a `override_mechanical` increment |
| The trust check fails while publishing the override | No publication either way, exactly as today — the review path hits the same error | Next poll retries; unchanged by this spec |
| `SendCommand` fails while publishing the override | No cursor entry is recorded, so the override is retried next poll and no review task is published in the failing poll | Confirm the next poll publishes the override and `override_mechanical` increments by 1 |
| `AddLabel` fails | No override is published; the PR falls back to a review this poll, so an approve never lands without its arming | Next poll retries the label write |
| `EnableAutoMerge` fails after a successful label write | The override is still published; the label is now present, so the next poll's existing arming path retries and succeeds | Confirm `IncPRPublished("auto_merge")` in the next poll; the approve is not stranded |
| A human pushes a non-mechanical commit onto a bot's bump branch | The classifier returns false, so a real review is published — the mechanical path never approves unreviewed content | None needed |
| The author is on the mechanical list but not trusted | No override; a normal review is published | Add the login to `TRUSTED_AUTHORS` or accept the review |
| The override approve is posted and the classifier was wrong | The approve is irreversible: the watcher cannot retract a review. A human dismisses it or a new head SHA supersedes it | Operator dismisses the review; the defect is a classifier bug, fixed in its own spec |
| GitHub rate-limits the per-PR config and file reads, or the label write | The failing read or write means "does not qualify", so the PR falls back to a review this poll | Next poll retries; the review covers the PR meanwhile |
| The unreviewed scanner sees the PR still awaiting a verdict and re-publishes a review task | A review task may appear alongside the override; the override approve still lands, and a review cannot un-approve a merged PR | Accepted: the scanner's retry window is far longer than the override's seconds-scale run; a duplicate review is noise, not a wrong merge |
| Repo has `autoMerge.trivial` true but `prReviewer.autoApprove` false | No override; normal review | Operator decides; the config is the switch |
| Two watcher replicas poll the same PR | Both compute the same task identifier; the task controller dedupes on the title path, so one task file exists | None needed |

## Security / Abuse

- The mechanical path can only **approve and arm**, never merge, and only for a login on an operator-configured allowlist that must also be trusted. Getting an arbitrary author onto it requires a values-file change, reviewed like any other deploy change.
- The classifier is the real control: it accepts only version and hash lines in a fixed file allow-list, so an author who somehow posts as an allowlisted bot still cannot get a non-mechanical change approved.
- The approve satisfies branch protection but does not bypass it: required status checks still gate the merge, and the ruleset is unchanged.
- A dependency bump to a compromised module version merges without an LLM reading the diff. That is an accepted trade, taken twice by the operator: an LLM reading `go.sum` hashes cannot detect a compromised upstream anyway.
- No credential is read, written or logged by this change.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `MECHANICAL_OVERRIDE_AUTHORS` config value with exact-login matching, and the six-condition qualification gate | 1 | 4, 5, 6, 9 (trust case) | — |
| 2 | The override emission: `override_reason` frontmatter, the rule-naming body, and no review command for a qualifying SHA | 2 | 1, 2 | prompt 1 |
| 3 | Label application and arming in the emitting poll, the dedupe cursor, and the error fall-through | 2, 4, 5 | 3, 8, 9 | prompt 2 |
| 4 | The log line, the `override_mechanical` counter label and its init-list registration, and the label-override precedence assertion | 3, 6 | 7, 10 | prompt 2 |

Rationale: prompt 1 establishes the config surface and the gate; prompt 2 builds the emission on it; prompts 3 and 4 are independent additions on top of prompt 2 and may run in either order. The Post-Deploy AC (11) is verified after the dev deploy, not by a prompt.

This spec is deliberately **not** split at the emission/arming seam, despite 6 Desired Behaviors × 11 Acceptance Criteria: an emission that ships without the label write and the arming leaves an approve that can never merge, which is the exact failure this spec exists to remove. The four prompts bound the research, and the classifier half is already its own spec.

## Do-Nothing Option

Every trivial bump keeps costing one LLM review and one queue slot. While the reviewer fleet is backlogged those PRs wait hours for a verdict on a change the watcher has already proven mechanical, so the cheapest change class becomes the slowest one.
