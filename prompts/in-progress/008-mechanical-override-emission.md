---
status: approved
spec: [002-mechanical-approve-trivial-bot-bumps]
created: "2026-10-24T08:31:00Z"
queued: "2026-10-24T09:13:04Z"
---

# Publish one mechanical `pr-override` task for a qualifying trivial bot bump

<summary>
- A pull request that passes the mechanical gate now publishes exactly one override task instead of a review task for that commit.
- The override task carries a reason field naming the rule, so the review agent can post a mechanical approve without running an LLM review.
- Its body names the rule and states that no LLM review ran; it never reuses the label-override wording, which claims a human applied a label.
- The override task keeps the same task kind and title shape as the label override, so it never collides with a review task file for the same commit.
- Deduplication is shared with the existing override path: a repeated poll on the same commit emits nothing, and a new commit emits once.
- If the command cannot be sent, nothing is recorded, so the override is retried on the next poll and no review is published in the failing poll.
- One operator-facing log line names the pull request, the commit and the rule on every emission.
- A new counter label makes the emission visible on the dashboard, pre-created at zero so alerts see the series before the first event.
</summary>

<objective>
Turn a qualified gate decision into the override emission: a `pr-override` command whose frontmatter carries `override_reason: trivial-bot-bump`, whose body names the rule and states that no LLM review ran, published at most once per (PR, head SHA) and never alongside a review task for that SHA.
</objective>

<context>
This prompt builds directly on the previous prompt in this series (`1-mechanical-override-config-and-gate`), which added `MECHANICAL_OVERRIDE_AUTHORS`, `pkg.ParseMechanicalOverrideAuthors`, the `mechanicalOverrideAuthors` watcher field, `qualifiesForMechanicalOverride`, and the gate evaluation in `processPR`. Read that code first — it is the unit you extend.

Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-logging-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before writing code:
- `pkg/watcher.go` — `TaskPublisher` (its `PublishOverride` doc comment is the contract shape to mirror), `taskPublisher`, `PublishOverride`, `NewTaskPublisher`, `tryOverride` (the cursor-dedupe pattern), `BuildOverrideCommand`, `buildOverrideFrontmatter`, `buildOverrideBody`, `DeriveTaskIDOverride` (in `pkg/taskid.go`), and `processPR` (where the gate is currently evaluated).
- `pkg/taskid.go` — `DeriveTaskIDOverride(owner, repo string, number int, sha string) uuid.UUID`.
- `pkg/metrics.go` — `IncPRPublished` and its doc comment, and the `init()` loop that pre-creates every `command` label at zero.
- `pkg/watcher_test.go` — `Describe("override label")` (the existing override assertions) and the `Describe("Mechanical override for trivial bot bumps")` container added by the previous prompt.
- `mocks/task_publisher.go` — the generated fake. It is regenerated, never hand-edited.

The `TaskPublisher` interface lives at the top of `pkg/watcher.go` with a `//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher` directive, and `pkg/pkg_suite_test.go` carries the `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` directive. Adding an interface method therefore requires regenerating `mocks/task_publisher.go`.
</context>

<requirements>
1. **Add the rule constant** in `pkg/watcher.go`, next to `buildOverrideFrontmatter`:

```go
// overrideReasonTrivialBotBump is the frontmatter `override_reason` value that
// tells the review agent this override is mechanical: no LLM review ran, and
// the body names the rule. It is the only reason the watcher emits today.
const overrideReasonTrivialBotBump = "trivial-bot-bump"
```

2. **Add the mechanical body builder** in `pkg/watcher.go`, directly after `buildOverrideBody`:

```go
func buildMechanicalOverrideBody(pr PullRequest) string
```

   Contract — the returned string MUST:
   - contain the literal `trivial-bot-bump`;
   - contain the literal sentence `No LLM review ran.`;
   - NOT contain the literal `override-review` (the label name must never appear in a mechanical body);
   - NOT contain the literal `A trusted author applied the override label.`;
   - keep the same overall shape as `buildOverrideBody`: an `# PR Override: <title>` heading, the PR URL, one explanatory paragraph, and a `**Repo:** [owner/repo](url)` line.

   Use `fmt.Sprintf` with the same `repoLink` construction `buildOverrideBody` uses. A suitable explanatory paragraph is: "Mechanical approve: this PR qualifies as a `trivial-bot-bump` — a bot dependency bump whose changed files are all mechanical. No LLM review ran. The watcher applied the auto-merge label and armed GitHub-native auto-merge; GitHub merges once required checks are green." Keep every code line within the repo's 100-column formatting (`golines --max-len=100` runs in precommit).

3. **Add the mechanical command builder** in `pkg/watcher.go`, directly after `BuildOverrideCommand`:

```go
// BuildMechanicalOverrideCommand builds the `pr-override` CreateTaskCommand for
// a qualifying mechanical override. It reuses BuildOverrideCommand for the
// title, task identifier, phase and clone fields — so the task path stays
// identical to the label override and the controller's title-path dedupe
// collapses the two — then stamps the reason and replaces the body.
func BuildMechanicalOverrideCommand(
	pr PullRequest,
	details PRDetails,
	taskIDStr string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
) task.CreateCommand {
	cmd := BuildOverrideCommand(
		pr, details, taskIDStr, stage, maxSlugLen, maxTitleLen, taskSuffix, targetVault,
	)
	cmd.Frontmatter["override_reason"] = overrideReasonTrivialBotBump
	cmd.Body = buildMechanicalOverrideBody(pr)
	return cmd
}
```

   Do not duplicate the `task.CreateCommand` literal — the wrapper is deliberate, so the two override kinds cannot drift on title, phase, assignee, or clone fields.

4. **Extend the `TaskPublisher` interface** in `pkg/watcher.go`, directly after `PublishOverride`, with a doc comment in the same style:

```go
	// PublishMechanicalOverride publishes the mechanical `pr-override` task for
	// a qualifying trivial bot bump. The caller (the watcher's mechanical gate)
	// has already evaluated the author list, trust, the repo's two opt-in flags
	// and the file classifier, so this method performs no trust check of its
	// own. Returns true when the command was sent; false when the send failed,
	// in which case the caller records nothing so the emission is retried on
	// the next poll.
	PublishMechanicalOverride(
		ctx context.Context,
		pr PullRequest,
		taskIDStr string,
		details PRDetails,
	) bool
```

5. **Implement it** on `*taskPublisher` in `pkg/watcher.go`, directly after `PublishOverride`:
   - Build the command with `BuildMechanicalOverrideCommand` using the same `p.cfg` fields `PublishOverride` uses.
   - On `p.createSender.SendCommand` error: `glog.Errorf("publish mechanical override task failed pr=%s err=%v", pr.HTMLURL, err)`, `p.metrics.IncPRPublished("error")`, return `false`.
   - On success: `glog.Infof("mechanical override pr=%s/%s#%d sha=%s rule=%s", pr.Owner, pr.Repo, pr.Number, details.HeadSHA, overrideReasonTrivialBotBump)` — one operator-facing line, deliberately not `V`-gated, because the spec's post-deploy check greps for it — then `p.metrics.IncPRPublished("override_mechanical")`, return `true`.

6. **Register the metric label.** In `pkg/metrics.go`, add `"override_mechanical"` to the `for _, cmd := range []string{...}` list in `init()` (the list currently ends with `"auto_merge_labeled"`). Extend the `IncPRPublished` doc comment's command list to include `"override_mechanical"`.

7. **Regenerate the mocks.** After changing the interface, run `ROOTDIR=/workspace make generate` (it wipes `mocks/` and regenerates every counterfeiter fake, including `mocks/task_publisher.go`). Never hand-edit `mocks/task_publisher.go`. The regenerated fake must expose `PublishMechanicalOverrideCallCount`, `PublishMechanicalOverrideArgsForCall`, `PublishMechanicalOverrideReturns`, and `PublishMechanicalOverrideReturnsOnCall`.

8. **Replace the gate-only block in `processPR`** with the emission call. The previous prompt left this block directly after the `tryOverride` guard:

```go
	if w.qualifiesForMechanicalOverride(ctx, pr) {
		glog.V(2).Infof(...)
	}
```

   Replace it with:

```go
	// Mechanical override: a qualifying trivial bot bump gets an approve instead
	// of a review for this SHA. Emitting both would race — a review could post
	// CHANGES_REQUESTED after the override's APPROVE and re-block merge.
	if w.tryMechanicalOverride(ctx, pr, details, cursorState.HeadSHAs, newHeadSHAs) {
		return pr.UpdatedAt, true
	}
```

9. **Add `tryMechanicalOverride`** in `pkg/watcher.go`, directly after `tryOverride`:

```go
func (w *watcher) tryMechanicalOverride(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
	cursorHeadSHAs map[string]string,
	newHeadSHAs map[string]string,
) bool
```

   Contract:
   - If `w.qualifiesForMechanicalOverride(ctx, pr)` is false, return `false` (the caller falls through to the normal review).
   - Compute `overrideID := DeriveTaskIDOverride(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()` — the SAME identifier `tryOverride` uses, so the two override kinds share one dedupe entry per (PR, head SHA).
   - If `cursorHeadSHAs[overrideID]` already exists: `glog.V(3).Infof` naming the PR and SHA, set `newHeadSHAs[overrideID] = details.HeadSHA`, return `true` (handled — no review for this SHA).
   - Otherwise call `w.publisher.PublishMechanicalOverride(ctx, pr, overrideID, details)`. When it returns `true`, record `cursorHeadSHAs[overrideID] = details.HeadSHA` and `newHeadSHAs[overrideID] = details.HeadSHA`. Return `true` whether or not the send succeeded — a failed send must NOT fall through to a review for this SHA, and must leave the cursor unset so the next poll retries.
   - Do not write labels, do not arm auto-merge, do not call any other GitHub API. Those are the next prompt's job.

10. **Tests — `pkg/watcher_test.go`, inside the existing `Describe("Mechanical override for trivial bot bumps")` container.** Reuse the helper and fixtures added by the previous prompt (`newTestWatcherWithMechanicalOverride`, the qualifying PR, the opted-in config, the mechanical `go.mod` file). Add exactly these three entries with these exact names:

   - `It("qualifying bump -> override task with rule body", ...)` — one poll on a qualifying PR. Assert:
     - `Expect(createSender.SendCommandCallCount()).To(Equal(1))` — one command, so no `pr-review` was published for the SHA;
     - `_, cmd := createSender.SendCommandArgsForCall(0)`; `cmd.Frontmatter["task_type"]` is `"pr-override"`;
     - `cmd.Frontmatter["override_reason"]` is `"trivial-bot-bump"`;
     - `cmd.Frontmatter["ref"]` is the head SHA `"deadbeef"`;
     - `cmd.Frontmatter["task_identifier"]` equals `pkg.DeriveTaskIDOverride("bborbe", "repo", 10, "deadbeef").String()`;
     - `cmd.Title` has prefix `"PR Override github - bborbe-repo - 10 - deadbeef"`;
     - `cmd.Body` contains `"trivial-bot-bump"`, contains `"No LLM review ran"`, does not contain `"override-review"`, and does not contain `"A trusted author applied the override label."`.
   - `It("dedupe per head SHA", ...)` — poll once (one command), poll again with the same details (still one command), then `ghClient.GetPRDetailsReturns(pkg.PRDetails{HeadSHA: "cafebabe", CloneURL: "https://github.com/bborbe/repo.git", BaseRef: "master"}, nil)` and poll a third time (two commands). Assert the second command's `Frontmatter["ref"]` is `"cafebabe"` and its `task_identifier` equals `pkg.DeriveTaskIDOverride("bborbe", "repo", 10, "cafebabe").String()`.
   - `It("emitting poll records override_mechanical", ...)` — one poll on a qualifying PR. Iterate `fakeMetrics.IncPRPublishedCallCount()` and `fakeMetrics.IncPRPublishedArgsForCall(i)`, counting occurrences of `"override_mechanical"`; assert the count is exactly `1`.

   Add a sibling entry OUTSIDE the `Mechanical override for trivial bot bumps` container, in the `Describe("Gate error paths")` container the previous prompt created (its name deliberately avoids the focus words `Mechanical override`, so the spec's focused run still selects only its named entries):
   - `It("send fails -> override retried, no review", ...)` — set `createSender.SendCommandReturnsOnCall(0, errors.New("kafka down"))`, poll once, assert `SendCommandCallCount()` is `1` and that call's `Frontmatter["task_type"]` is `"pr-override"` (no review fallback); then `createSender.SendCommandReturns(nil)`, poll again, assert a second send whose `Frontmatter["task_type"]` is `"pr-override"` — the retry proves no cursor entry was recorded on the failure.

11. **Do not change** `PublishOverride`, `BuildOverrideCommand`, `buildOverrideFrontmatter`, `buildOverrideBody`, `tryOverride`, or `buildFrontmatter`. The label path must keep its body and must keep having no `override_reason` key.

12. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement above against the diff.
</requirements>

<constraints>
- **The mechanical override carries its own body.** It reuses the override task type but not the label wording. Its body names the rule and states that no LLM review ran; the label body's sentence `A trusted author applied the override label.` must never appear in it, and the label name `override-review` must not appear either.
- **No review task for a qualifying SHA.** Emitting both races: a review could post CHANGES_REQUESTED after the override's APPROVE and re-block the merge.
- **Dedupe is per (PR, head SHA) and shared with the existing override dedupe** — use `DeriveTaskIDOverride` and the same cursor maps, never a new key space.
- **A failed send is handled, not ignored.** It records nothing (so it retries next poll) and returns handled=true (so no review is published in the failing poll).
- **Never merge.** No merge API call is added; native auto-merge on green remains the only merge path.
- **No trust check in `PublishMechanicalOverride`.** The gate owns it; a second check would be a second source of truth.
- **`override_mechanical` is registered at zero** in `pkg/metrics.go`'s `init()` list, alongside the other `command` labels.
- **No label write and no arming in this prompt.** The next prompt in this series adds them.
- **Prompts 2 and 3 ship in one release.** This prompt publishes an approve that prompt 3 arms. Do not deploy, merge or release between them: an approve with nothing armed is the stranded-approve failure this feature removes.
- **The webhook / single-PR trigger path is out of scope.** `pkg/command/trigger_pr_review_executor.go` publishes reviews via `BuildCreateCommand` and does not consult this gate, exactly as it does not consult the override label today. The mechanical override is a poll-loop behaviour. If the stage registers the `pull_request` webhook, a review task may coexist with the override for the same SHA — the same accepted noise the spec's failure-mode table records for the unreviewed scanner. Do not add a gate there in this prompt.
- Do not hand-edit any file under `mocks/`; regenerate with `ROOTDIR=/workspace make generate`.
- Do not run `go mod tidy`, `go get`, or `go mod vendor`.
- **Release ordering (operator-side, per stage).** Deploy `bborbe/github-pr-review-agent` PR #53 (which understands `override_reason: trivial-bot-bump`) before the watcher, and ship the `go.sum` classifier change in the same watcher release — without it `ClassifyTrivial` rejects every real bump and this feature stays dark. Do not change `pkg/trivial.go` here.
- Do not commit — dark-factory handles git.
- Existing tests must still pass, including every `Describe("override label")` entry and every entry the previous prompt added.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.

Then run the focused spec run and confirm the four entries above are listed and 0 failures:

```
go test -mod=mod ./pkg/ -v -ginkgo.focus='Mechanical override'
```

Then confirm the emission is wired and the metric label exists at both the call site and the init list:

```
grep -n 'trivial-bot-bump' pkg/watcher.go
grep -n 'override_mechanical' pkg/metrics.go pkg/watcher.go
grep -n 'PublishMechanicalOverride' pkg/watcher.go mocks/task_publisher.go
```
</verification>
