---
status: approved
approved: "2026-11-09T17:40:34Z"
branch: dark-factory/bug-retry-guard-never-learns-outcome
---

## Summary

- The retry guard suppresses a forced re-trigger against an in-memory ledger record, and nothing ever tells it that the review it recorded has failed.
- The record therefore suppresses for its whole life, and its life ends only by a silent prune at the one-hour staleness bound, or by a pod restart. Neither path tells the operator anything.
- The fix makes the guard consume the task lifecycle event stream and clear the record as soon as the review task reaches a terminal status, so a failed review releases its suppression immediately instead of waiting out the bound.
- The suppression itself is unchanged: a review that is genuinely still running keeps suppressing, because no terminal event has arrived for it.

## Problem

A forced re-trigger of a failed review is accepted, returns a green `requestID`, and then publishes nothing. The operator has no signal that the request was refused: the gateway responds before any task exists, and the only evidence is a `retry guard: suppressed in-flight run` line in the watcher's log.

The guard's record is written when a review is published and carries only the task identifier and the publish time. It is never reconciled with what happened to that task. So a review that failed at minute two still suppresses at minute fifty-nine, and the operator's retry — the one action that exists for a stuck review — does nothing.

## Goal

When a review task reaches a terminal status, the guard record for that task is removed, and the next forced trigger for the same commit publishes instead of being suppressed. A review that has not reached a terminal status keeps suppressing exactly as it does today.

## Non-goals

- Changing the suppression window, the staleness bound, or the prune behaviour.
- Changing when a record is written, or what it stores.
- Making a forced trigger synchronous, or changing the `/trigger` response.
- Replacing the in-memory ledger with durable storage, or fixing its per-replica limitation.
- Draining the reviews already stuck behind a suppressed record; that is operational work on a separate task.
- Filtering cross-vault events as an optimisation — matching by task identifier is correct, and the ledger is small.

## Reproduction

**Version.** `bborbe/github-pr-watcher` `v0.23.1`, read from the `github-pr-watcher` StatefulSet image on nuke prod (namespace `agent`) on 2026-11-09. `origin/master` was `be3e53b`, one docs-only merge past that tag; the retry guard is unchanged between them.

**Smallest configuration.** One open pull request whose head SHA carries no reviewer-bot verdict, one watcher instance, two forced triggers. No Kafka tuning, no cluster change, no config edit.

**Fixture.** `bborbe/github-pr-watcher#68` at head `1b6089e619746542ab831ecfed420d9e35a62f2a` — open, and its only reviewer-bot verdict (`CHANGES_REQUESTED`) sat at an older SHA `17b3002c`. The head therefore carries no verdict, so the guard's verdict-override path cannot fire and the measurement isolates the record's lifetime.

**Steps.**

```bash
PR=https://github.com/bborbe/github-pr-watcher/pull/68
TRIGGER=~/Documents/Obsidian/private-personal/.claude/scripts/trading-api-write.sh

# t=0 — publish, which writes the ledger record
$TRIGGER prod "/api/1.0/command/maintainer-githubprreview-v1/trigger-pr-review" \
  '{"url":"'"$PR"'","force":true}'

# t=+5m27s — the retry the operator would actually make
$TRIGGER prod "/api/1.0/command/maintainer-githubprreview-v1/trigger-pr-review" \
  '{"url":"'"$PR"'","force":true}'

kubectlnukeprod -n agent logs github-pr-watcher-0 --since=10m | grep -E 'retry guard|published CreateTaskCommand'
```

**Observed — T1, the defect.** Both calls returned a green `requestID` (`58390898-c5f2-41a2-a24c-d5199977c61d` then `28d75d22-1e06-4634-8daf-c665b95e5030`). The second produced no task:

```
I1109 15:07:44.944875 1 retry_guard.go:221] retry guard: suppressed in-flight run
  pr=bborbe/github-pr-watcher#68 sha=1b6089e… task_id=0f50975c-f017-5240-9a47-181773c85b53 age_seconds=326
```

`age_seconds=326` matches the 5m26s between the two triggers exactly. Pod `metadata.uid=2fe6efad-411d-4b46-929a-b26b99f09cf3`, `status.startTime=2026-11-08T19:44:27Z` — unchanged throughout, so no restart confound.

**Observed — T2, the release.** At `2026-11-09T16:16:15Z`, +74 min after the publish, a third forced trigger **published** (`task_id=59faecd0-d049-557d-9323-c106b42c0888`) with **no `retry guard:` line of any kind**. The one branch that logs nothing is the no-record branch — so the record had been **pruned**, not declared stale.

**Measured bound: `(326 s, 74 min]`, released by prune.** The scanner never re-published for that PR (no `unreviewed retry` line between the probes), so nothing refreshed the record, and the ledger prunes any record past the one-hour bound on the next write.

## Expected vs Actual

**Expected.** A retry is an operator asking for one review of one commit. The guard's record exists to stop a *second concurrent* review of a commit whose first review is still running; once that first review has ended, the record has no purpose. The staleness bound's own documented rationale is that "no legitimate run outlives this bound" — it is a backstop for a crashed run, not the intended release path for a completed or failed one.

**Actual.** The record's only exits are the prune and a pod restart. A review that fails seconds after publishing keeps its record for the full hour, and the release is silent: no log line, no metric, no operator signal. The measured release at T2 came from the prune, and the operator would have seen nothing at all.

## Why this is a bug

The forced re-trigger is the only action an operator has for a stuck review, and this defect makes it a no-op while reporting success. The failure is silent in both directions — the gateway returns green, and the release emits nothing — so there is no way to tell "refused" from "slow" from "nothing to do".

It is also a producer of the records goal SC7 counts. SC7 (reworded 2026-11-09) counts a record stuck once it has waited **beyond 60 minutes** with no `current_job` and no verdict. The measured suppression window `(326 s, 74 min]` straddles that bound, and it ends in a silent prune — so a forced re-trigger during the window manufactures exactly the record SC7 fails on.

## Acceptance Criteria

- [ ] **AC1 — a terminal event clears the record.** A task event whose identifier matches a ledger record, and whose task carries a terminal status, removes that record. Evidence: a test feeding the event through the consumer's own handler (not by calling the clear directly) asserting the record is gone and a subsequent `Check` for the same commit returns `Reason: create` rather than `skip_inflight`.

- [ ] **AC2 — each terminal status clears, and no other status does.** The clear fires for status `completed` and for status `aborted`. It does **not** fire for `in_progress`, `next`, `backlog` or `hold`. Evidence: a table-driven test over the six statuses asserting a clear for exactly the two terminal ones and no clear for the other four; the `in_progress` case additionally asserts a subsequent `Check` still returns `skip_inflight`. This is the negative control that stops the consumer clearing the record the create event just wrote.

- [ ] **AC3 — an unknown task identifier is a silent no-op.** An event whose identifier matches no record removes nothing. Evidence: a unit test asserting no clear occurred and a subsequent `Check` is unchanged; the zero-line log assertion is made at the deployed rung (AC8), where real logs exist — this repository has no log-capture helper, the same limitation spec 004 records.

- [ ] **AC4 — the clear names what it cleared.** The clear emits, at default verbosity (`glog.Infof`, not `glog.V(2)`), one line of exactly the shape `retry guard cleared reason=task_outcome_<outcome> pr=<owner>/<repo>#<n> sha=<sha> task_id=<id>`, where `<outcome>` is `failed` when the task's `## Diagnostics` block carries `outcome: failed`, `completed` when it carries `outcome: success`, and the terminal status otherwise. Evidence: a table-driven test rendering the line for each outcome class — `outcome: failed` → `reason=task_outcome_failed`, `outcome: success` → `reason=task_outcome_completed`, an aborted task with no diagnostics outcome → `reason=task_outcome_aborted`; and `outcome: skipped` → `reason=task_outcome_skipped` — so a hardcoded single token fails it; plus `grep -rnE '^\s*glog\.Infof\("retry guard cleared reason=task_outcome_' pkg/ --include='*.go'` returns ≥ 1, anchored at line start so the literal is emitted rather than merely mentioned in a comment.

- [ ] **AC5 — the consumer subscribes to the prefixed task event topic.** The topic is derived, not hardcoded: `lib.TaskV1SchemaID.EventTopic(topicPrefix)`. Evidence: a unit test asserting the derived topic equals `develop-agent-task-v1-event` for prefix `develop` and `agent-task-v1-event` for an empty prefix, plus a test that a real `cdb.EventObject` produced by the sender is decoded back into a task by the consumer's handler.

- [ ] **AC6 — the suppression path is not bypassed.** The fix clears the record; it does not make a forced trigger skip the guard, and it does not shorten the staleness bound. Evidence: `git diff origin/master -- pkg/retry_guard.go | grep -E '^[-+].*retryGuardStaleAfter'` returns empty, and the existing `Check` tests pass unchanged.

- [ ] **AC7 — a genuinely in-flight run is still suppressed.** A record whose task has produced no terminal event still suppresses. Evidence: a test with a record and only non-terminal events asserting `Reason: skip_inflight`, and — post-deploy — a forced re-trigger inside the review's own run window still logging `retry guard: suppressed in-flight run`.

- [ ] AC8 — **Post-Deploy (Rung-2):** on nuke dev (namespace `agent`), a review task that reaches a terminal status without a verdict produces one `retry guard cleared reason=task_outcome_` line naming its task identifier and no `retry guard cleared` line for a task that is still in progress, and a forced re-trigger for that commit afterwards publishes a new task rather than suppressing. Evidence: `kubectlnukedev -n agent logs statefulset/github-pr-watcher --since=30m | grep -c 'retry guard cleared reason=task_outcome_.*<task_id>'` returns ≥ 1; the same grep over a still-running task's identifier returns 0 (the negative control); and the subsequent forced trigger emits `published CreateTaskCommand … taskID=` for that PR, which the watcher logs at `-v=2` — read from the running pods on 2026-11-09, `kubectlnukeprod -n agent get pod github-pr-watcher-0 -o jsonpath='{.spec.containers[0].args}'` returning `["-v=2"]` on prod and the dev pod matching; the `args` come from the OCI chart, not from `values-*.yaml`, so re-read the pod rather than the values file if this probe ever stops matching.
  - `deploy_check:` `kubectlnukedev -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

- [ ] AC9 — **Post-Deploy (Rung-3):** on nuke prod (namespace `agent`), the same two probes as AC8, run against the prod StatefulSet and the prod image: one `retry guard cleared reason=task_outcome_` line for the failed task identifier, zero for a still-running one, and a subsequent forced trigger that emits `published CreateTaskCommand … taskID=` for that PR.
  - `deploy_check:` `kubectlnukeprod -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `ROOTDIR=/workspace make precommit` — format, lint and vet clean, exit 0
- `ROOTDIR=/workspace make test` — full suite passes, including every AC1–AC7 test
- `grep -rnE '^\s*glog\.Infof\("retry guard cleared reason=task_outcome_' pkg/ --include='*.go'` returns ≥ 1
- `grep -rn 'TaskV1SchemaID' pkg/ --include='*.go'` returns ≥ 1 — the topic is derived, not a bare literal
- `grep -c '^## Unreleased' CHANGELOG.md` returns 1 — the fix's bullet has a section to live in

### Host-executable (runs on the host after the prompts complete, before the PR)

- `git diff --exit-code origin/master -- pkg/taskid.go` returns empty — identifier derivation is not modified
- `git diff origin/master -- pkg/retry_guard.go | grep -E '^[-+].*retryGuardStaleAfter'` returns empty — the bound is unchanged

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- Replay the `## Reproduction` steps against the deployed fix: the second forced trigger must publish instead of logging `suppressed in-flight run` once the task has reached a terminal status.
- Merge the fix, cut the release tag by hand (`autoRelease: false`), then bump the watcher tag in `bborbe/nuke` `github-pr-reviewer/` `values-dev.yaml` and its `MIRROR_IMAGES` entry
- `/nuke-deploy dev github-pr-reviewer`, then `kubectlnukedev -n agent get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}'` — must equal the release tag
- Walk AC8 on dev, then `/nuke-deploy prod github-pr-reviewer` (operator's own keystroke) and walk AC9 on prod
- A pod restart is not evidence: the ledger is in-memory, so a restart clears every record. Record `metadata.uid` and `status.startTime` before and after each probe and discard the run if either changed.

## Desired Behavior

1. When a review task reaches a terminal status (`completed` or `aborted`), the guard record carrying that task's identifier is deleted.
2. The delete emits one line naming the outcome class, the pull request, the head SHA and the task identifier, at default verbosity.
3. A task at any non-terminal status (`in_progress`, `next`, `backlog`, `hold`) emits no clear and leaves its record in place, so an in-flight review keeps suppressing.
4. An event whose identifier matches no record is a no-op and logs nothing.
5. A delete event, which carries no task payload, is ignored explicitly rather than logged as a decode error.
6. After a clear, the next forced trigger for that commit publishes and writes a fresh record.
7. The consumer runs alongside the existing poll loop, HTTP server and command consumer, and never returns on a recoverable error — a return would cancel the whole group.
8. The staleness bound, the prune behaviour and the suppression decision are unchanged.

## Constraints

- **The signal is the prefixed task event topic.** It is `lib.TaskV1SchemaID.EventTopic(a.TopicPrefix)` — `develop-agent-task-v1-event` under a prefix, `agent-task-v1-event` with none. The bare literal is not the subscribed name: a consumer hardcoded to it receives nothing, silently, while every unit test still passes.
- **The event key is the task identifier.** `EventObject.ID` is `base.EventID(task.TaskIdentifier)` — the same UUID string the ledger record already stores, so the join is exact and needs no new field.
- **The status is read from the task's frontmatter**, not a top-level field of the event: the payload is a `lib.Task` whose status lives in `frontmatter.status`. The terminal set is exactly `{completed, aborted}`. `hold` is **not** terminal — it is a pause with a scheduled resume — and neither are `in_progress`, `next` or `backlog`.
- **The outcome token is derived from the diagnostics, not assumed.** `reason=task_outcome_failed` when the task's `## Diagnostics` block carries `outcome: failed`; `reason=task_outcome_completed` when it carries `outcome: success`; `reason=task_outcome_skipped` when it carries `outcome: skipped`; the terminal status otherwise. The producer emits exactly those three values. A clear on a successful completion must not read `failed` in the log the operator greps. The block is a real artifact with a real producer: `bborbe/github-pr-review-agent` writes it via `appendDiagnosticsSection` (`pkg/steps_checkout_execution.go`), and it appears in 11,034 records under `private-agent/tasks/` — the default `bborbe/agent` result deliverer writes `## Result` instead, so the reviewer agent is the producer to check against.
- **The consumer never returns on a recoverable error.** `main.go` composes with `run.CancelOnFirstFinish`, which cancels every sibling when one function completes — so a consumer that returns tears down the poll loop, the HTTP server and the command consumer. It retries internally and returns only when its context is cancelled.
- **The ledger stays in-memory**, and a restart is still the only immediate way to clear a stuck record. This remains true and stays documented.
- **The record is keyed `owner/repo#N@sha` and stores the task identifier.** Clearing by task identifier scans the bucket and deletes the matching record; the log line derives `pr` and `sha` from that key.
- **Cross-vault events are expected, and no filter is added for them.** The topic carries every task in the stage, from every vault; matching by task identifier makes that correct, and the ledger holds at most a few hundred records, so a scan per event is not worth a config branch.
- **Never clear on a non-terminal event.** The create event arrives moments after the record is written; clearing on it would disable the guard entirely.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`.
- **The suppression path is not bypassed.** `retryGuardStaleAfter`, the prune rule and the decision table are unchanged.
- **The CHANGELOG has no `## Unreleased` section.** `autoRelease: false`, so the bullet needs a new `## Unreleased` heading above the newest released section.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection |
|---|---|---|---|
| The event topic is unavailable | The consumer retries from its offset; suppression continues as today | none needed — the staleness bound still releases the record | consumer lag on the event topic |
| A terminal event arrives before the record is written | The clear finds nothing and logs nothing; the record then suppresses for its full life | the staleness bound releases it; a restart clears it | the absent `retry guard cleared` line for a task that did end |
| A non-terminal event is misclassified as terminal | The guard clears a live record and a duplicate review can publish | none needed — bounded by the 30-minute Job deadline | two tasks for one commit with no suppression line |
| A delete event arrives | Ignored explicitly; no decode-error line | none needed — the record ages out | no clear and no decode error for that task |
| Two events for one task arrive concurrently | The first deletes, the second is a no-op | none needed | one `retry guard cleared` line, not two |
| The watcher restarts between the publish and the terminal event | The ledger is empty, so the clear is a no-op | none needed — nothing is left to suppress | the record's absence |
| The consumer returns on an error | `CancelOnFirstFinish` cancels the poll loop, HTTP server and command consumer; the pod restarts and the ledger empties | none needed — the restart clears every record, which is a known limitation | pod restart count and the consumer's own exit |

## Security / Abuse

The consumer reads a Kafka topic the fleet already trusts, on the watcher's existing broker connection and schema. It writes only to the watcher's in-process ledger, opens no network connection and exposes no new surface. A malformed or hostile event can at worst delete one ledger record, which can only make the guard more permissive — the pre-fix behaviour for that record.

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | Clearing a record by task identifier: bucket scan, delete, the derived outcome token and its fixed log literal, unit tests including the unknown-identifier no-op and the unchanged-bound negative | 1, 2, 4 | 1, 3, 4, 6 | — |
| 2 | The outcome consumer: derive the topic from the schema id and prefix, decode the event into a task, classify the terminal status set, ignore delete events, call the clear, wire it as a fourth `run.Func` without letting it return on error, tests including the non-terminal and in-flight negative controls | 3, 5, 6, 7, 8 | 2, 5, 7 | prompt 1 |

AC8 and AC9 are verification-phase criteria: the operator walks them against the deployed StatefulSets after the release tag is cut, and no prompt implements them.

## Do-Nothing Option

The defect keeps costing exactly what it costs today: a forced re-trigger of a failed review is accepted, acknowledged green, and publishes nothing for up to an hour, and the release is a silent prune that tells nobody. The operator's only recovery is to wait, or to restart the watcher — which also drops every other record in the ledger. Against SC7's 60-minute stuck-record bound, a one-hour silent suppression is itself a producer of the records the goal counts.
