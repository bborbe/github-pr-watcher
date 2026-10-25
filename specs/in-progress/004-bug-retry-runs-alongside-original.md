---
status: prompted
approved: "2026-10-25T09:26:29Z"
generating: "2026-10-25T17:18:47Z"
prompted: "2026-10-25T17:28:32Z"
branch: dark-factory/bug-retry-runs-alongside-original
---

## Summary

- A forced re-review of a pull request publishes a second task for the same commit even when the first task's Job is still running, so two reviewer pods work the same head SHA at the same time.
- Only the forced path can do this. An unforced duplicate is rejected upstream, and the retry title is precisely what lets the duplicate through.
- The fix makes the forced path consult a record of what it has already published for that repository, pull request and head SHA, and suppress the publish while that run is still in flight.
- The suppression is observable only in the watcher's log, its metric and the task list. The `/trigger` endpoint is fire-and-forget by design and returns a fixed 202 body that names no task, before or after this fix.
- A commit that already carries a verdict still re-runs, and a record older than one hour stops suppressing, so a crashed run cannot block retries indefinitely.

## Problem

Retrying a stuck PR review is a normal operator action, and it is supposed to help a review land sooner. Today it can make the review slower and less reliable: the retry is published while the original is still executing, both pods review the same head SHA, and whichever finishes last overwrites the other's verdict. The observable cost is a duplicated reviewer workload on a queue that is already deep, and two independent verdicts racing to land on one commit.

## Goal

A forced trigger on a commit that already has a run in flight while the watcher still holds a fresh record for that commit is suppressed: nothing is published, the watcher logs a line naming the existing task, and the `inflight_skipped` counter moves. A forced trigger on a commit whose run has finished, or whose verdict has already landed, behaves exactly as it does today. The three remaining windows in which a duplicate can still occur — a watcher restart, a second replica, and a forward wall-clock jump — are named in Failure Modes: a restart is bounded by the 30-minute Job deadline, a second replica by the single-replica precondition, and a clock jump by the clock settling (the one-hour staleness rule then applies to real time again).

## Reproduction

**Version.** `bborbe/github-pr-watcher` `v0.12.1`, read from the `github-pr-watcher` StatefulSet image on both nuke dev and nuke prod on 2026-10-23. That is three minor releases behind the repository HEAD (`CHANGELOG.md` tops out at `v0.15.1`); the forced path is unchanged between them — `pkg/command/trigger_pr_review_executor.go` still publishes unconditionally at HEAD. The dark-factory version is not part of this reproduction: the defect is in the watcher and is triggered over its HTTP endpoint, not by a dark-factory run.

**Smallest configuration.** One pull request with a stable head SHA, one watcher instance, two forced triggers. No Kafka tuning, no cluster change.

**Fixture.** Any open pull request in a trusted repository whose author is trusted by the deployed `TRUSTED_AUTHORS` allowlist — otherwise the run stops at `applyTrust` and never reaches `publishCreateCommand`, which looks identical to a working guard. The watcher's own repository is sufficient. Record the fixture's pull request number and its head SHA at trigger time.

**Steps.** Run against nuke dev, substituting the deployment's own trigger route and the fixture's PR URL:

```bash
BASE=https://dev.quant.benjamin-borbe.de/admin/maintainer-watcher-github-pr
PR=https://github.com/bborbe/github-pr-watcher/pull/<number>
SHA=<head sha of PR, from gh pr view --json headRefOid>

curl -sS -X POST "$BASE/trigger?url=$PR&force=true"; echo
sleep 2
curl -sS -X POST "$BASE/trigger?url=$PR&force=true"; echo

kubectlnukedev -n dev logs statefulset/github-pr-watcher --since=15m | grep -c "$SHA"
```

**Observed.** Both POSTs return the identical 202 body, `{"status":"accepted","url":"<pr url>","force":true}` — the response is written before any task exists and says nothing about either task (`pkg/handler/trigger_handler.go:59-71`, `:110-118`; the executor runs with `SendResultEnabled = false`, `pkg/command/trigger_pr_review_executor.go:60`, so there is no result event either). The task listing, not the response, is where the duplicate appears: two records and two Jobs exist for the one head SHA.

**Observed in production, same defect at natural cadence.** Over the fourteen days ending 2026-10-22T17:12:11Z, 34 pairs of records exist for an unchanged head SHA, and in 5 of them the two runs overlapped. The five are inlined here because they are the population that justifies the fix; the full 34-pair table and the record query that produced it live in the anchor task page's `# Results`:

| Pair | Head SHA | Why the two runs overlapped |
|---|---|---|
| `ip#8` | unchanged across both records | the original was still live when the retry was published |
| `validation#13` | unchanged across both records | the original was reset in the same pass |
| `alert-controller#11` | unchanged across both records | the original was reset in the same pass |
| `memorykv#17` | unchanged across both records | a controller relaunch landed after the retry existed |
| `kafka-topic-backup#17` | unchanged across both records | a controller relaunch landed after the retry existed |

The 34-pair population breaks down as 5 concurrent and 29 sequential: a sequential pair is a re-run after a verdict, which is the sanctioned case the guard must keep allowing.

The retry's own record is what makes the duplicate possible: the agent controller rejects a duplicate by title path, and an unforced duplicate is refused with `ErrTaskAlreadyExists` (observed in production on `bborbe/coding#90`, 2026-08-09). `pkg/filename.go:68-70` is the only place that appends the ` - retry-<id8>` token to a title, and the forced path is the only caller that salts the task identifier.

## Expected vs Actual

**Expected.** A retry is an operator asking for one review of one commit. While that commit's review is already running, the retry should report the run that exists rather than start a second one. The forced path exists to re-run a review that has finished — `pkg/factory/factory.go:146-151` documents the salt as the mechanism for deriving a distinct task identifier when `Force=true` — and a distinct identifier is a way to re-run a completed review, not a licence to run two at once.

**Actual.** The forced path publishes unconditionally. `pkg/command/trigger_pr_review_executor.go` derives a time-salted identifier from `currentDateTime.Now().UnixMicro()` and sends the create command without consulting whether a run for that repository, pull request and head SHA is already in flight or already has a verdict. Its only skip conditions are an invalid URL, a filter skip, an untrusted author and the v0.15.1 webhook deferral — and the last never applies here, because its gate is `cmd.Webhook && !cmd.Force` (`trigger_pr_review_executor.go:147`).

## Workaround

Until the fix ships, trigger the retry without `force=true`: the unforced path derives the deterministic `DeriveTaskID` identifier, so the controller silently rejects a duplicate of an existing task with `ErrTaskAlreadyExists` (documented at `trigger_pr_review_executor.go:306`). The cost is that an unforced trigger cannot re-review a head SHA whose task already exists — which is exactly the stuck-review case the forced path exists for.

## Why this is a bug

The forced path's own contract is re-review, and the system has no other way to express "run this one more time" — so an operator has no way to retry a stuck review without risking a duplicate. The duplicate is not benign: two pods reviewing one head SHA can publish two verdicts for one commit, and the losing verdict is written after the winning one. The one-hour staleness rule the guard needs is already implied by the deployment: prod pr-review Jobs carry `activeDeadlineSeconds: 1800` (read live on nuke prod, 2026-10-23), so no Job outlives thirty minutes and the longest measured live overlap is 765 s — a one-hour bound cannot suppress a legitimate retry.

## Acceptance Criteria

### Guard decisions and ledger (unit level)

- [ ] **AC1 — in-flight duplicate is suppressed.** A forced decision for a repository, pull request and head SHA that has a published record younger than one hour, with no verdict at that head SHA, returns a skip: no create command is sent, `metrics.IncPRPublished("inflight_skipped")` is incremented, and the returned error wraps `cdb.ErrCommandObjectSkipped` and names the existing task identifier. Evidence: a table-driven test asserting (a) the sender recorded zero sends, (b) the counter moved on the `inflight_skipped` label, (c) `errors.Is(err, cdb.ErrCommandObjectSkipped)` is true, (d) the returned decision's `Reason` is `skip_inflight` and its `ExistingTaskID` is the first publish's identifier.

- [ ] **AC2 — a verdict at the head SHA re-runs.** The same decision, when a review at that head SHA exists from a login in the configured reviewer set, returns create, and the caller logs one line containing `retry guard: force overrode verdict` naming the pull request and head SHA. Evidence: a test asserting the sender recorded exactly one send and the decision's `Reason` is `create_force_overrode_verdict`. The log line is **not** asserted at unit level: `glog` binds stderr at init and this repository has no log-capture helper (`grep -rn capture --include='*_test.go'` finds only the rate-limit capture), so the literal is asserted at the deployed rung, where real logs exist.

- [ ] **AC3 — a moved head SHA is unaffected (negative control).** A forced decision for a head SHA that has no published record returns create, and logs neither `retry guard: force overrode verdict` nor `retry guard: stale in-flight record ignored`. Evidence: a test asserting exactly one send and a `Reason` of `create` — not `create_force_overrode_verdict` and not `create_stale_record_ignored`. The `grep -c` = 0 form of this control moves to the deployed rung, where the logs are real.

- [ ] **AC4 — a stale record does not suppress.** A forced decision for a head SHA whose published record is older than one hour returns create, and logs one line containing `retry guard: stale in-flight record ignored` naming the record and its age in seconds. Evidence: a test asserting exactly one send and a `Reason` of `create_stale_record_ignored` carrying the record's age.

- [ ] **AC5 — the unforced path is untouched.** A non-forced decision performs no ledger read, no verdict lookup and emits no guard log line: the guard is not consulted at all on that path. Evidence: a test with a guard whose methods record calls, asserting zero calls, plus the existing non-forced tests passing unchanged. Its positive counterpart binds the wiring: a forced decision driven through `runTriggerPRReview` calls the guard exactly once, so a guard that is implemented but never reached from the executor fails this AC.

- [ ] **AC6 — a failed send leaves no record.** A forced decision whose create send fails writes no record for that head SHA, so a second forced decision for the same commit returns create rather than skip. Evidence: a test whose sender returns an error, followed by a second decision, asserting the second decision returns create.

- [ ] **AC7 — a successful send records the publish.** After one successful forced publish, a second forced decision for the same head SHA returns skip with the first decision's task identifier. Evidence: a test asserting one send, then a skip naming that identifier — the positive counterpart to AC6, so the ledger write cannot be left to the deployed rung alone.

### Deployed behaviour

Fixture values for AC8–AC11, set once in the operator's shell before walking the rungs (the placeholders below are these variables):

```bash
PR_NUM=<open PR number in a trusted repo by a trusted author>
REPO=bborbe/github-pr-watcher
SHA=$(gh -R "$REPO" pr view "$PR_NUM" --json headRefOid --jq .headRefOid)
SHA8=${SHA:0:8}
TITLE_PREFIX=${REPO//\//-}
VAULT=$(kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="TARGET_VAULT")].value}')
```

`<sha>` below is `$SHA`, `<sha8>` is `$SHA8`, `<old-sha>` is the AC8 `$SHA`, and `<new-sha>` is the value re-read after pushing a commit to the fixture PR. `$VAULT` is the dev watcher's `TARGET_VAULT` (the vault its create commands land in); for AC11 re-read it with `kubectlnukeprod -n prod`.

- [ ] AC8 — **Post-Deploy (Rung-2):** on nuke dev, two forced triggers at least 2 s apart for one unchanged fixture head SHA produce exactly one record and one Job for that SHA, and the watcher logs exactly one `retry guard: suppressed in-flight run` line naming the first task's identifier. Evidence, three probes: (a) `vault-cli task list --vault "$VAULT" | grep -c "$TITLE_PREFIX.*$SHA8"` returns 1, (b) the Job listing returns 1 Job for that SHA, (c) `kubectlnukedev -n dev logs statefulset/github-pr-watcher --since=15m | grep -c 'retry guard: suppressed in-flight run.*<sha>'` returns 1.
  - `deploy_check:` `kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

- [ ] AC9 — **Post-Deploy (Rung-2):** on nuke dev, once that head SHA's review has landed a verdict, a forced trigger creates its record and Job as before, and the watcher logs `retry guard: force overrode verdict` for that SHA — evidence: the new record exists, and `kubectlnukedev -n dev logs statefulset/github-pr-watcher --since=15m | grep -c 'retry guard: force overrode verdict.*<sha>'` returns ≥ 1.
  - `deploy_check:` `kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

- [ ] AC10 — **Post-Deploy (Rung-2):** on nuke dev, a forced trigger for a fixture whose head has moved to a new SHA creates its record and Job as before, and the suppression line is absent for the new SHA while the same grep over AC8's window returns ≥ 1 for the old SHA. Evidence, two distinct SHAs: `kubectlnukedev -n dev logs statefulset/github-pr-watcher --since=30m | grep -c 'retry guard: suppressed in-flight run.*<new-sha>'` returns 0, and the same command with `<old-sha>` returns ≥ 1 (known-positive control proving the grep sees suppression lines at all).
  - `deploy_check:` `kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

- [ ] AC11 — **Post-Deploy (Rung-3):** on nuke prod, AC8 repeats against the deployed prod image: two forced triggers at least 2 s apart for one unchanged fixture head SHA produce exactly one record and one Job, and `kubectlnukeprod -n prod logs statefulset/github-pr-watcher --since=15m | grep -c 'retry guard: suppressed in-flight run.*<sha>'` returns ≥ 1, with the task-list count for that SHA returning 1.
  - `deploy_check:` `kubectlnukeprod -n prod get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0 --contains HEAD)`

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `ROOTDIR=/workspace make precommit` — format, lint and vet clean, exit 0
- `ROOTDIR=/workspace make test` — the full suite passes, including every AC1–AC7 test
- `grep -rnE '^\s*glog\.Infof\("retry guard: force overrode verdict' pkg/ --include='*.go'` returns ≥ 1 — anchored at line start, so the literal is emitted, not merely mentioned in a comment
- `grep -rnE '^\s*glog\.Infof\("retry guard: stale in-flight record ignored' pkg/ --include='*.go'` returns ≥ 1
- `grep -rnE '^\s*glog\.Infof\("retry guard: suppressed in-flight run' pkg/ --include='*.go'` returns ≥ 1
- `grep -c '"inflight_skipped"' pkg/metrics.go` returns 2 — the doc comment and the `init()` loop, the two places the label must be registered
- `grep -c '^## Unreleased' CHANGELOG.md` returns 1 — the fix's bullet has a section to live in
- `grep -cE 'lost on restart|in-memory' pkg/retry_guard.go` returns ≥ 1 — the restart limitation is documented where the ledger is implemented

### Host-executable (runs on the host after the prompts complete, before the PR)

- `BASE=f787877` (origin/master when the worktree was created, `v0.15.1`), then `git diff --exit-code "$BASE" -- pkg/taskid.go` returns empty — the identifier derivation is not modified. It runs on the host because `hideGit` masks `.git` inside the container; the base ref is mandatory, since without one the diff compares worktree to index and passes trivially once the change is committed.

### Operator-executable (runs on the host after PR merge, spec verification ladder)

- Merge the fix, cut the release tag that carries it, then bump `WATCHER_TAG_dev` in `Makefile` and `watcher.tag` in `values-dev.yaml` in `bborbe/nuke` `github-pr-reviewer/`
- Run every `deploy_check:` from a checkout of master **after** the merge, never from the spec branch: `git describe --tags --abbrev=0 --contains HEAD` fails with `fatal: No tags can describe` when HEAD is not an ancestor of a release tag, and `autoRelease: false` means the tag is cut by hand
- `cd ~/Documents/workspaces/nuke-dev` → `git fetch && git merge --ff-only origin/master` → `cd github-pr-reviewer && make apply`
- `kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}'` — must equal the release tag above
- `kubectlnukedev -n dev get statefulset github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].env[*].name}' | grep -c REVIEWER_BOT_LOGINS` returns 1 — the verdict half of the guard is dead without it
- The four dev fixtures and the prod fixture, run against the deployed StatefulSets
- The prod `make apply` is the operator's own keystroke

## Desired Behavior

1. A forced trigger for a repository, pull request and head SHA that already has a published record younger than one hour, and no verdict at that head SHA, publishes nothing, increments the `inflight_skipped` publish counter, and returns an error wrapping `cdb.ErrCommandObjectSkipped` that names the existing task identifier.
2. A forced trigger for a head SHA that already carries a review from a configured reviewer login publishes exactly as today, and logs `retry guard: force overrode verdict` naming the pull request and head SHA.
3. A forced trigger for a head SHA with no published record publishes exactly as today, with no guard log line.
4. A forced trigger for a head SHA whose published record is older than one hour publishes exactly as today, and logs `retry guard: stale in-flight record ignored` naming the record and its age.
5. The unforced path is behaviourally unchanged: no ledger read, no verdict lookup, no new log line.
6. A record is written only after the create command has been sent successfully, so a failed send never suppresses the next attempt.

## Constraints

- **The reviewer set is the existing one.** The verdict check must use the same login-matching rule and the same configured logins as the unreviewed-PR scanner, so a verdict that satisfies the scanner also satisfies the guard. No new environment variable and no new config field.
- **No new external state.** The ledger lives in the watcher's existing in-process key-value store, keyed directly on repository, pull request and head SHA. No new Kafka topic, no new schema, no new dependency, no PVC.
- **The ledger is lost on restart, and that is acceptable.** The store is in-memory (`main.go:368`, `db := pkg.NewMemDB()`), the consumer replays from `OffsetOldest`, and prod pr-review Jobs carry `activeDeadlineSeconds: 1800` with a longest measured overlap of 765 s, so a restart can only widen the window in which one duplicate slips through. The verdict check is the restart-independent backstop. This limitation must be stated in the new code's doc comment, together with the fact that a restart is also the only immediate way to clear a stuck record.
- **The staleness bound is one hour**, measured from the matching record's publish time.
- **Three fixed log literals**, each at default verbosity via `glog.Infof` (not `glog.V(2)`, so the observable does not depend on a `-v` flag the deployment may not set): `retry guard: suppressed in-flight run`, `retry guard: force overrode verdict`, `retry guard: stale in-flight record ignored`. Each line also carries the head SHA, and the first and third carry the task identifier and the age.
- **A new metric label, registered in two places.** `inflight_skipped` is added to the `IncPRPublished` doc comment at `pkg/metrics.go:22-24` *and* to the `init()` pre-initialisation loop at `pkg/metrics.go:122`, which carries fourteen values (the comment lists seven, so the two lists have already drifted — do not copy the comment's list into the loop, add to each). It must not reuse `skipped` — that label already means a filter or trust skip, and an operator must be able to tell a duplicate suppression from a filtered PR.
- **The CHANGELOG has no `## Unreleased` section.** `CHANGELOG.md` opens directly at `## v0.15.1`, and the repo runs `autoRelease: false`, so the fix's bullet needs a new `## Unreleased` heading above `## v0.15.1`. Do not fold the bullet into a released section: with no releaser running, an entry under `## v0.15.1` is invisible to the next release.
- **The webhook deferral of v0.15.1 is untouched.** The executor's webhook flag and its deferred label stay as they are; the guard only runs on the forced path and the webhook handler never sets Force.
- **Identifier derivation is frozen.** `DeriveTaskID` and `DeriveTaskIDForce` are not modified, and the forced path keeps its time-salted identifier — that salt is what allows a legitimate re-run after a verdict.
- **Errors** are built with `github.com/bborbe/errors`; no `fmt.Errorf`.
- **Do not weaken the existing skip paths** — invalid URL, filter skip and untrusted author keep their current behaviour and ordering.
- **The HTTP surface does not change.** `/trigger` keeps its 202 body `{"status","url","force"}` byte-identical; the handler learns nothing about task creation and must not be made to wait for one.
- **An empty reviewer set suppresses everything fresh.** The deployed StatefulSet must carry `REVIEWER_BOT_LOGINS`; the operator rung verifies it, and the guard treats an empty set as "no verdict known".
- **The decision is typed, and its reason is the unit-level observable.** `Check` returns a decision carrying an action, a `Reason` and, on a skip, the existing task identifier and the record's age. The four reasons are exactly `create`, `create_force_overrode_verdict`, `create_stale_record_ignored` and `skip_inflight`. The unit ACs assert on `Reason`; the log literals are the deployed-rung observable and are emitted from the same value, so the two cannot drift.
- **The guard lives in package `pkg`.** `matchesReviewerLogin` is unexported (`pkg/unreviewed_scanner.go:459`), so a guard outside `pkg` would have to duplicate the login-matching rule — which the first constraint forbids. Name the file `pkg/retry_guard.go`.
- **The verdict lookup runs inside the executor's already-held consumer transaction.** The executor is invoked inside `cdb.RunCommandConsumerTxDefault`'s `memDB.Update` callback, and `memDB`'s `sync.RWMutex` is not re-entrant, so the guard opens no transaction of its own and never calls `DB.Update`/`View`; `ListReviews` runs inside that held transaction exactly as `GetPRDetails` already does. Amended during prompt audit: the original "outside the transaction" placement is unreachable without restructuring the consumer and would deadlock if attempted from the executor.
- **The StatefulSet runs one replica, and the guard is not correct across replicas.** Each pod would hold its own in-process ledger, so two replicas reproduce the duplicate. This is a precondition, not a property the guard establishes.
- **The ledger is pruned on write.** A record is deleted once it is older than the staleness bound, at the moment a newer record is written for any key, so the map cannot grow without bound over the process lifetime.
- **`force overrode verdict` means an override.** That reason and its log line are emitted only when a record exists *and* a verdict exists at the head SHA — the case where the guard had something to override. A forced trigger for a verdicted SHA with no surviving record publishes silently, because nothing was overridden.

## Failure Modes

| Trigger | Expected behavior | Recovery | Detection | Concurrency |
|---|---|---|---|---|
| Two forced triggers race within the same millisecond for one head SHA | Exactly one publishes; the second skips | none needed | the suppression line names the existing task | commands are processed sequentially by the single consumer goroutine, so the second trigger's read happens after the first's write; no lock spans the send |
| The create send fails after the guard allowed it | No record is written; the next forced trigger publishes | re-run the same `curl …?force=true`; the next attempt publishes | the executor's existing send-error line | no stale record to clear |
| The verdict lookup fails (GitHub error) | The guard does not suppress: the trigger publishes, and the lookup error is logged | none needed — publishing is the pre-fix behaviour | lookup-error line naming the pull request | an unavailable GitHub API can only make the guard permissive, never blocking |
| The watcher restarts with a run still in flight | The ledger is empty, so one forced trigger can duplicate | none needed — bounded by the 30-minute Job deadline | the duplicate is visible as two records for one head SHA | documented limitation |
| A run is stuck but its Job is still alive | The forced trigger is suppressed for up to one hour | the existing frontmatter reset re-runs the same record and publishes nothing new, so the guard does not interfere with it; deleting the Job lets the record age out; restarting the watcher clears the in-memory ledger at once | the suppression line names the task and its age | the guard keys on publish time, not on Job state |
| A record outlives its run (crash, cancelled Job) | After one hour the record stops suppressing and the retry publishes | automatic on the next forced trigger | `retry guard: stale in-flight record ignored` names the record and its age | the age check reads the stored publish time |
| The head SHA moves while a run is in flight | The new SHA has no record, so it publishes | none needed | no suppression line for the new SHA | records are keyed per head SHA |
| `REVIEWER_BOT_LOGINS` is empty or unset | Every fresh record suppresses, so a verdicted SHA cannot re-run either | set the variable on the StatefulSet | the suppression line fires even where a verdict exists | the guard treats an empty set as "no verdict known" |
| The wall clock jumps forward past one hour while a record is fresh | The record reads stale and the retry publishes | automatic once the clock settles | `retry guard: stale in-flight record ignored` names an implausible age | age is computed from the wall-clock publish time |
| The wall clock jumps backward, so a publish time sits in the future | The age reads negative, so the record suppresses longer than one hour | none needed — the skew is bounded by clock correction | no line fires where one is expected | negative ages are clamped to fresh, so the record is suppressed, never skipped as stale |
| Two watcher replicas run | Each pod holds its own ledger, so a duplicate can still occur | scale back to one replica | two records for one head SHA, with no suppression line | the ledger is per-process; the StatefulSet's single replica is a precondition |

## Suggested Decomposition

Prompts should be generated in this order — each row is a single prompt with a clear scope.

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | The guard itself: the three-way decision (no record / verdict / fresh / stale), the ledger and its pruning, the verdict lookup, the typed reason, the three log literals, the metric label, unit tests | 1–4, 6 | 1–7 | — |
| 2 | Wiring: thread the transaction and the guard through the executor, the factory and `main.go`, running the verdict lookup inside the consumer's held transaction (no nested `Update`/`View`); plus the executor-level integration test and the untouched-unforced-path assertion | 5 | 5, and AC1–AC4 observed once through the executor | prompt 1 |

AC8–AC11 are verification-phase criteria: they are walked by the operator against the deployed StatefulSets after the release tag is cut, and no prompt implements them.

Rationale: prompt 1 is pure package logic with no signature churn and its ACs are testable in isolation. Prompt 2 is the wiring that makes the guard reachable in production, and it is the only prompt that touches an existing constructor signature, so keeping it separate keeps prompt 1 reviewable on its own. Counted across the whole spec the size is 6 behaviours × 11 ACs = 66, above the 50 split threshold; the split is what brings each prompt back under it — prompt 1 carries the pure decision logic, prompt 2 only the wiring and one integration test, and AC8–AC11 are verification-phase and land in neither. The spec is deliberately at the upper end of the size budget because the decision table is one indivisible unit — a guard that skips without recording, or records without checking a verdict, is not a smaller version of this fix, it is a different and broken one.
