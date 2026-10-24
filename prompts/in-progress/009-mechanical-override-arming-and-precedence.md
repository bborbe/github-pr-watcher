---
status: approved
spec: [002-mechanical-approve-trivial-bot-bumps]
created: "2026-10-24T08:32:00Z"
queued: "2026-10-24T09:13:04Z"
---

# Arm auto-merge in the mechanical-override poll and keep the label override in front

<summary>
- The poll that publishes a mechanical approve now also applies the auto-merge label and arms GitHub-native auto-merge, so the merge still happens without a human.
- Arming never depends on the separate trivial-auto-merge switch; the mechanical path writes its own label and arms itself.
- A pull request that already carries the auto-merge label is not labelled or armed a second time in the same poll.
- If the label write fails, no approve is published and the pull request falls back to a normal review that poll, so an approve never lands without its arming.
- If arming fails after the label was written, the approve is still published; the next poll's existing arming path retries and succeeds.
- An explicit human override label still wins: a labelled pull request is never routed through the mechanical path, and its task keeps the label wording with no reason field.
- The operator documentation and the changelog describe the new environment variable, the two repo opt-in flags, and the release ordering the feature depends on.
</summary>

<objective>
Complete the mechanical override by writing the auto-merge label and arming native auto-merge in the same poll that publishes the approve, and lock in the precedence rule that an explicit override label always beats the mechanical gate. This closes the one failure the feature exists to remove: an approve that can never merge because nothing armed the pull request.
</objective>

<context>
This prompt builds on the previous two prompts in this series (`1-mechanical-override-config-and-gate` and `2-mechanical-override-emission`), which added the qualification gate, `tryMechanicalOverride`, `BuildMechanicalOverrideCommand`, `PublishMechanicalOverride`, and the `override_mechanical` counter. Read that code first.

Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before writing code:
- `pkg/watcher.go` — `tryMechanicalOverride` (the function you extend), `tryOverride`, `maybeLabelTrivial` (the existing `AddLabel` call shape and its error handling), `tryAutoMerge` (the existing `EnableAutoMerge` call shape and its metrics), `processPR` (the side-effect ordering: `trySupersedeDepBump` → `tryUpdateBranch` → `tryAutoMerge` → `tryOverride` → `tryMechanicalOverride`), and `buildOverrideBody`.
- `pkg/githubclient.go` — `AddLabel` and `EnableAutoMerge` doc comments (both require Pull requests: Write; re-arming an already-armed PR succeeds; arming a clean PR fails with UNPROCESSABLE).
- `pkg/watcher_test.go` — the `Describe("Mechanical override for trivial bot bumps")` and `Describe("Gate error paths")` containers, the `newTestWatcherWithMechanicalOverride` helper, and `Describe("override label")` (the label-path assertions to mirror).
- `README.md` — the `## Environment Variables` table (the `AUTO_MERGE_LABEL` row is the neighbour to add after) and the `## Auto-merge arming window` section (the new section goes before it).
- `CHANGELOG.md` — the preamble and the top version heading.
</context>

<requirements>
1. **Label and arm inside `tryMechanicalOverride`.** In `pkg/watcher.go`, after the cursor-dedupe check returns and before calling `w.publisher.PublishMechanicalOverride(...)`, add the label-and-arm step:

```go
	if !slices.Contains(pr.Labels, w.autoMergeLabel) {
		if err := w.ghClient.AddLabel(
			ctx, pr.Owner, pr.Repo, pr.Number, w.autoMergeLabel,
		); err != nil {
			glog.Errorf("mechanical override: add label failed pr=%s err=%v", pr.HTMLURL, err)
			w.metrics.IncPRPublished("error")
			return false
		}
		if err := w.ghClient.EnableAutoMerge(ctx, pr.Owner, pr.Repo, pr.Number); err != nil {
			glog.Errorf(
				"mechanical override: enable auto-merge failed pr=%s err=%v", pr.HTMLURL, err,
			)
			w.metrics.IncPRPublished("error")
		}
	}
```

   Semantics, all load-bearing:
   - `w.autoMergeLabel` is guaranteed non-empty here: the gate rejects an empty label (previous prompt). Do not add a second emptiness check.
   - **Label absent** → `AddLabel` then `EnableAutoMerge`. `AddLabel` failure returns `false` (the caller falls back to a normal review this poll — an approve must never land without its arming). `EnableAutoMerge` failure is logged and counted but does NOT stop the override: the label is now present, so the next poll's existing `tryAutoMerge` path retries and succeeds.
   - **Label already present** → neither call. `tryAutoMerge` already ran earlier in this same poll with that label present and armed the PR (or logged its failure); a second arming would be a redundant API call and can fail with UNPROCESSABLE on an already-clean PR.
   - Do not add a new metric label for the label write or the arming. The next poll's `tryAutoMerge` already counts `auto_merge`, and the emission itself counts `override_mechanical`.

2. **Keep the rest of `tryMechanicalOverride` unchanged**: gate first, then dedupe, then label/arm, then `PublishMechanicalOverride`, recording both cursor maps only when the send succeeded. Return `true` once the gate qualified **and** the label write (when one was needed) succeeded, so no review is published for that SHA; return `false` on an `AddLabel` failure so the caller falls through to a normal review this poll (requirement 1). A failed `EnableAutoMerge` after a successful label write does **not** change the return value — the override is still published and the next poll's `tryAutoMerge` retries the arming.

3. **Tests — `pkg/watcher_test.go`.** Add exactly these entries with these exact names inside the existing `Describe("Mechanical override for trivial bot bumps")` container, reusing `newTestWatcherWithMechanicalOverride` and its fixtures:

   - `It("qualifying bump -> label present and auto-merge armed", ...)` — one poll on a qualifying PR with no labels. Assert:
     - `Expect(ghClient.AddLabelCallCount()).To(Equal(1))`; `_, owner, repo, number, label := ghClient.AddLabelArgsForCall(0)` with `owner == "bborbe"`, `repo == "repo"`, `number == 10`, `label == "auto-merge"` (this is how "the label is present afterwards" is observed with the fake);
     - `Expect(ghClient.EnableAutoMergeCallCount()).To(Equal(1))`;
     - `Expect(createSender.SendCommandCallCount()).To(Equal(1))` and `cmd.Frontmatter["task_type"] == "pr-override"`;
     - this test runs with `trivialAutoMergeEnabled: false`, which is what proves arming does not depend on `TRIVIAL_AUTO_MERGE`.
   - `It("override label present -> label override only", ...)` — a qualifying PR that also carries the `"override-review"` label. Build the watcher with `autoMergeLabel: "auto-merge"` and the mechanical list containing the author (pass `"override-review"` where the helper needs the override label — see note below), and set `ghClient.GetMaintainerConfigReturns(optedIn, nil)` and `ghClient.ListPRFilesReturns(mechanical, nil)` so the mechanical gate *would* pass. Assert:
     - `Expect(createSender.SendCommandCallCount()).To(Equal(1))`;
     - `cmd.Frontmatter["task_type"] == "pr-override"`;
     - `_, hasReason := cmd.Frontmatter["override_reason"]; Expect(hasReason).To(BeFalse())` — the label path must never carry the mechanical reason;
     - `Expect(cmd.Body).To(ContainSubstring("A trusted author applied the override label."))`;
     - `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(0))` and `Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))` — the mechanical gate was never reached.

     Note on the helper: the previous prompt's `newTestWatcherWithMechanicalOverride` does not take an override label. Extend it with a final `overrideLabel string` parameter (pass `""` in every existing call) and pass it through to `pkg.NewWatcher` in the `overrideLabel` position. Do not add a second helper.

   Add these entries OUTSIDE the `Mechanical override for trivial bot bumps` container, in the existing `Describe("Gate error paths")` container (its name deliberately avoids the focus words `Mechanical override`, so the spec's focused run still selects only its named entries):
   - `It("add label fails -> review only", ...)` — qualifying PR, `ghClient.AddLabelReturns(errors.New("label write failed"))`. Assert `Expect(ghClient.AddLabelCallCount()).To(Equal(1))`, `Expect(ghClient.EnableAutoMergeCallCount()).To(Equal(0))`, `Expect(createSender.SendCommandCallCount()).To(Equal(1))` and that the command is a `pr-review` — the pull request falls back to a review this poll.
   - `It("enable auto-merge fails -> override still published", ...)` — qualifying PR, `ghClient.AddLabelReturns(nil)`, `ghClient.EnableAutoMergeReturns(errors.New("clean status"))`. Assert `Expect(ghClient.EnableAutoMergeCallCount()).To(Equal(1))`, `Expect(createSender.SendCommandCallCount()).To(Equal(1))` and the command is a `pr-override` with `Frontmatter["override_reason"] == "trivial-bot-bump"`.
   - `It("pre-labeled qualifying bump -> no second label or arming", ...)` — qualifying PR whose `Labels` already contains `"auto-merge"`. Assert `Expect(ghClient.AddLabelCallCount()).To(Equal(0))`, `Expect(ghClient.EnableAutoMergeCallCount()).To(Equal(1))` (the earlier `tryAutoMerge` call, not a second one), and the override is still published.

4. **README.** In `## Environment Variables`, add a row directly after the `AUTO_MERGE_LABEL` row:

```markdown
| `MECHANICAL_OVERRIDE_AUTHORS` | no | — | Comma-separated GitHub logins that may be approved without an LLM review on a repo that opts in through **both** `.maintainer.yaml` flags (`autoMerge.trivial` + `prReviewer.autoApprove`). Exact, case-sensitive match. Empty disables the path. Not the same list as `BOT_ALLOWLIST` (skip) or `TRUSTED_AUTHORS` (may be reviewed) — a login here must also be trusted |
```

   Then add a new section, `## Mechanical override for trivial bot bumps`, immediately before `## Auto-merge arming window`. It must state:
   - what qualifies (author on `MECHANICAL_OVERRIDE_AUTHORS`, author trusted, repo opted in through both flags, every changed file mechanical per `ClassifyTrivial`, not a draft, no override label);
   - what happens in the emitting poll (auto-merge label applied, native auto-merge armed, one `pr-override` task with `override_reason: trivial-bot-bump` and a body stating no LLM review ran, and no review task for that head SHA);
   - that the watcher never merges — GitHub performs the merge once required checks are green;
   - that dedupe is per (PR, head SHA) and shared with the label override;
   - the release ordering: `bborbe/github-pr-review-agent` must understand `override_reason: trivial-bot-bump` before the watcher is deployed, otherwise the agent posts the label wording;
   - that the `go.sum` classifier must be in the same watcher release, or `ClassifyTrivial` rejects every real bump and the feature stays dark;
   - the failure fall-throughs: a config/file read error or a failed label write means "review this poll", a failed arming still publishes the approve and is retried next poll;
   - that an empty list is the off switch and there is no separate enable flag.

5. **CHANGELOG.** If `## Unreleased` exists, append one bullet to it; otherwise insert a `## Unreleased` section immediately after the preamble (below the sentence ending "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).") and above the top `## vX.Y.Z` heading. Exactly one bullet, prefixed `feat:`:

```markdown
- feat: mechanically approve trivial bot dependency bumps on opted-in repos — apply the auto-merge label, arm native auto-merge, and publish one `pr-override` task carrying `override_reason: trivial-bot-bump` instead of an LLM review
```

6. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement above against the diff.
</requirements>

<constraints>
- **Never merge.** No merge API call is added; native auto-merge on green remains the only merge path.
- **The mechanical path performs its own label write and its own arming.** It must not depend on `TRIVIAL_AUTO_MERGE` or on `maybeLabelTrivial` having run.
- **Label precedence.** A pull request carrying the override label is handled by `tryOverride` and never reaches the mechanical path. Its task keeps the existing label body and must have no `override_reason` key.
- **An approve never lands without its arming.** A failed label write returns `false` so the pull request is reviewed this poll; only a failed arming (after a successful label write) is tolerated, because the next poll retries it.
- **No second arming.** When the auto-merge label is already present, do not call `AddLabel` and do not call `EnableAutoMerge` from the mechanical path.
- **Existing behavior must not regress:** the label-override path, the review path, the auto-merge labelling and arming for PRs that do not qualify, and supersession are unchanged.
- **No credential is read, written or logged** by this change.
- **The classifier stays conservative.** Do not touch `pkg/trivial.go`; `go.sum` support ships in its own spec.
- Do not run `go mod tidy`, `go get`, or `go mod vendor`.
- **Release ordering (operator-side, per stage).** Deploy `bborbe/github-pr-review-agent` PR #53 (which understands `override_reason: trivial-bot-bump`) before the watcher, and ship the `go.sum` classifier change in the same watcher release — without it `ClassifyTrivial` rejects every real bump and this feature stays dark. Do not change `pkg/trivial.go` here.
- Do not commit — dark-factory handles git.
- Existing tests must still pass, including every entry added by the two previous prompts.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.

Then run the focused spec run and confirm the two arming/precedence entries above are listed and 0 failures:

```
go test -mod=mod ./pkg/ -v -ginkgo.focus='Mechanical override'
```

Then confirm the documentation landed:

```
grep -n 'MECHANICAL_OVERRIDE_AUTHORS' README.md
grep -n 'Mechanical override for trivial bot bumps' README.md
grep -n 'trivial-bot-bump' CHANGELOG.md
```

The label-path and already-labelled assertions are covered by the entries `override label present -> label override only` and `pre-labeled qualifying bump -> no second label or arming` in the focused run above; no separate source-level grep is needed.
</verification>
