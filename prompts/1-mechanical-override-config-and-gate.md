---
status: draft
spec: ["002-mechanical-approve-trivial-bot-bumps"]
created: "2026-10-24T08:30:00Z"
branch: dark-factory/mechanical-approve-trivial-bot-bumps
---

# Gate a trivial bot bump on a mechanical-override author list and the repo's two opt-in flags

<summary>
- A repo can now list the bot logins that may be approved without an LLM review, through a new operator-set environment variable.
- The list is parsed with the same comma/trim rules as the trusted-authors list, but it is a distinct list with a distinct meaning: "may be approved without a review" is stronger than "trusted".
- The watcher can now prove, for a single pull request, that it is a qualifying trivial bot bump: the author is on the list, the author is trusted, the repo opted in through both of its configuration flags, and every changed file is mechanical.
- An empty list means nobody qualifies — that is the off switch, and no separate enable flag is added.
- Every check that does not hold falls through to today's normal review, unchanged.
- A failing configuration read or file listing means "does not qualify" and is counted as an error, so the pull request still gets a review.
- A failing trust check publishes nothing, exactly as today.
- The gate is evaluated in the poll loop after the label-override path, so an explicit human label always wins over the mechanical path.
- The approve itself is deliberately not built here: a qualifying pull request still gets a normal review in this step.
</summary>

<objective>
Introduce the `MECHANICAL_OVERRIDE_AUTHORS` configuration value and the six-condition qualification gate, so the watcher can prove a bot dependency bump is trivial and operator-approved before any approve path exists. This prompt stops at the decision: a qualifying pull request still receives today's normal review. The emission that follows the gate ships in the next prompt of this series.
</objective>

<context>
Read the project `docs/dod.md` and `CLAUDE.md` if present.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-composition.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before writing code:
- `pkg/watcher.go` — the `watcher` struct, `NewWatcher`, `processPR` (the filter → details → side effects → `tryOverride` → review sequence), `maybeLabelTrivial`, `tryAutoMerge`, `tryOverride`, and the `trust.PR` usage in `tryAutoMerge` / `tryUpdateBranch`. The new gate follows the same `w.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})` call shape and the same `glog.V(2).Infof` / `glog.V(3).Infof` logging style.
- `pkg/filter.go` — `ParseBotAllowlist` and `ParseTrustedAuthors`. The new parser follows the same split/trim rules.
- `pkg/trivial.go` — `ClassifyTrivial(files []PRFile) bool` and its doc comment (pure function, conservative).
- `pkg/trust/trust.go` — `Trust`, `PR`, `Result` (`Success()`).
- `pkg/githubclient.go` — `GetMaintainerConfig`, `ListPRFiles`, and the `PullRequest` / `PRDetails` / `PRFile` structs.
- `pkg/factory/factory.go` — `CreateWatcher`, the only production caller of `pkg.NewWatcher`.
- `main.go` — the `application` struct's argument/env tags and the `Run` wiring (`CreateWatcher` call).
- `pkg/watcher_test.go` — the `newTestWatcher*` helper family, `newDisabledUnreviewedScanner`, and the `Describe("trivial auto-merge label")` block, which is the closest existing exemplar for a `GetMaintainerConfig` + `ListPRFiles` + `ClassifyTrivial` test.
- `pkg/filter_test.go` — the `Describe("ParseTrustedAuthors")` block.
- `pkg/unreviewed_scanner_test.go` — the inline `pkg.NewWatcher(...)` call in the scan-wiring test (it also needs the new parameter).

`maintainerconfig.MaintainerConfig` on the pinned `github.com/bborbe/maintainer` v0.50.12 has exactly these fields (verified): `PrReviewer maintainerconfig.PrReviewerConfig` with `AutoApprove bool` (yaml `autoApprove`), and `AutoMerge maintainerconfig.AutoMergeConfig` with `Trivial bool` (yaml `trivial`). Do not invent additional fields.
</context>

<requirements>
1. **Add the login parser** in `pkg/filter.go`, directly after `ParseTrustedAuthors`:

```go
// ParseMechanicalOverrideAuthors splits a comma-separated
// MECHANICAL_OVERRIDE_AUTHORS string into trimmed, non-empty logins. The
// splitting rules are deliberately identical to ParseTrustedAuthors; the
// distinct name marks the distinct meaning — these logins may be approved
// without a review, which is stronger than "trusted" and must never be
// conflated with BOT_ALLOWLIST (skip) or TRUSTED_AUTHORS (may be reviewed).
func ParseMechanicalOverrideAuthors(raw string) []string {
	return ParseTrustedAuthors(raw)
}
```

2. **Add the application field** in `main.go`, in the `application` struct directly after the `TrivialAutoMerge` field, with the neighbouring fields' tag shape:

```go
	MechanicalOverrideAuthors string `required:"false" arg:"mechanical-override-authors" env:"MECHANICAL_OVERRIDE_AUTHORS" usage:"Comma-separated GitHub logins that may be approved without an LLM review on a repo opted in via .maintainer.yaml autoMerge.trivial + prReviewer.autoApprove. Empty disables the mechanical override path."`
```

Do not add a default: an empty value must stay empty (the off switch).

3. **Wire it in `main.go` `Run`.** In the `factory.CreateWatcher(...)` call, insert `pkg.ParseMechanicalOverrideAuthors(a.MechanicalOverrideAuthors)` directly after the `a.TrivialAutoMerge` argument. Keep the existing `//nolint:funlen` comment on `Run` accurate: if its stated line count changes, update the numbers in that comment.

4. **Extend `factory.CreateWatcher`** in `pkg/factory/factory.go`: add a `mechanicalOverrideAuthors []string` parameter directly after `trivialAutoMergeEnabled bool`, and pass it to `pkg.NewWatcher` in the same position. The factory stays zero-logic — no parsing, no conditionals.

5. **Extend `pkg.NewWatcher`** in `pkg/watcher.go`: add a `mechanicalOverrideAuthors []string` parameter directly after `trivialAutoMergeEnabled bool`, and store it on the `watcher` struct in a new field. Give the field a doc comment stating its meaning and its two neighbours' meanings:

```go
	// mechanicalOverrideAuthors is the exact, case-sensitive set of GitHub
	// logins that may be approved without an LLM review when the repo has
	// opted in. It is NOT BOT_ALLOWLIST (which means "skip entirely") and NOT
	// the trusted-authors list (which means "may be reviewed and may be
	// armed"). Empty means nobody qualifies — the off switch, with no
	// separate enable flag.
	mechanicalOverrideAuthors []string
```

6. **Update every `pkg.NewWatcher` / `factory.CreateWatcher` call site.** There are no others; the complete list is:
   - `main.go` — `factory.CreateWatcher` (requirement 3).
   - `pkg/factory/factory.go` — the `pkg.NewWatcher` call.
   - `pkg/watcher_test.go` — the four helpers `newTestWatcher`, `newTestWatcherWithOverride`, `newTestWatcherWithAutoMerge`, `newTestWatcherWithTrivialAutoMerge`; the inline `pkg.NewWatcher` call in the `Describe("TaskPublisher contract")` block; `pollWithSideEffectFilter` inside `Describe("labeled side-effect pass")`; and `newWiringWatcher` inside `Describe("unreviewed scan wiring")`.
   - `pkg/unreviewed_scanner_test.go` — the inline `pkg.NewWatcher` call in the scan-wiring test.

   Every existing call site passes `nil` for the new parameter (place it directly after the `false` / `true` that is `trivialAutoMergeEnabled`, i.e. `false, nil, trustDecision,` / `true, nil, trustDecision,`). Do not otherwise change those tests.

7. **Add the qualification gate** to `pkg/watcher.go`, directly after `maybeLabelTrivial` and before `BuildCreateCommand`:

```go
func (w *watcher) qualifiesForMechanicalOverride(ctx context.Context, pr PullRequest) bool
```

Contract — return `true` only when every condition below holds, evaluated in this exact order with short-circuiting:

   1. `w.autoMergeLabel != ""`. An empty auto-merge label disables arming fleet-wide, and a mechanical approve that cannot be armed is the stranded-approve failure this feature exists to remove, so it must not qualify. (This is a configuration precondition, not a seventh semantic gate.)
   2. `slices.Contains(w.mechanicalOverrideAuthors, pr.AuthorLogin)` — exact, case-sensitive byte equality, matching `trust.NewAuthorAllowlist`. `slices` is already imported.
   3. `w.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})` returns a nil error and `result.Success() == true`.
   4. `w.ghClient.GetMaintainerConfig(ctx, pr.Owner, pr.Repo)` returns a nil error and `cfg.AutoMerge.Trivial && cfg.PrReviewer.AutoApprove` (both flags, no default).
   5. `w.ghClient.ListPRFiles(ctx, pr.Owner, pr.Repo, pr.Number)` returns a nil error and `ClassifyTrivial(files) == true`.

   Failure behavior, exactly:
   - Author not listed: `glog.V(3).Infof` naming the PR and the login, return `false`. No GitHub call is made (condition 1 and 2 precede every API read).
   - Trust-check error: `glog.Errorf` naming the PR and the error, return `false`. Do NOT increment any counter — the review path's own trust check is what counts `error` today, and this must stay byte-identical to today's behavior.
   - Untrusted author: `glog.V(2).Infof` naming the PR, return `false`, no counter.
   - `GetMaintainerConfig` error: `glog.Errorf` naming the PR and the error, `w.metrics.IncPRPublished("error")`, return `false`.
   - Either config flag false: `glog.V(3).Infof` naming the repo, return `false`, no counter. `ListPRFiles` must NOT be called in this case.
   - `ListPRFiles` error: `glog.Errorf` naming the PR and the error, `w.metrics.IncPRPublished("error")`, return `false`.
   - `ClassifyTrivial` false: `glog.V(3).Infof` naming the PR, return `false`, no counter.
   - The function never mutates `pr`, never writes to GitHub, and never publishes a command.

8. **Evaluate the gate in the poll loop** in `processPR`, directly after the `if w.tryOverride(...) { return pr.UpdatedAt, true }` block and before `taskIDStr := DeriveTaskID(...)`:

```go
	// Mechanical override gate: a qualifying trivial bot bump will be approved
	// without an LLM review. The emission ships in the next prompt in this
	// series; evaluating the gate here keeps its negative paths observable now.
	if w.qualifiesForMechanicalOverride(ctx, pr) {
		glog.V(2).Infof(
			"mechanical override gate passed pr=%s/%s#%d sha=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA,
		)
	}
```

   The placement is load-bearing: the draft filter runs earlier in `taskCreationFilter`, and `tryOverride` runs immediately before it, so an explicit override label always wins over the mechanical gate. A qualifying pull request still falls through to the existing review publication in this prompt.

9. **Tests — `pkg/watcher_test.go`.** Add a new top-level container inside `Describe("pkg.Watcher")`, after the `Describe("trivial auto-merge label")` block, named exactly:

```go
	Describe("Mechanical override for trivial bot bumps", func() {
```

   Every `It` name below is load-bearing: the spec's focused run `-ginkgo.focus='Mechanical override'` selects them. Use exactly these names.

   Add a helper next to the existing `newTestWatcherWithTrivialAutoMerge`:

```go
func newTestWatcherWithMechanicalOverride(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	cursorPath string,
	startTime libtime.DateTime,
	fakeMetrics *mocks.Metrics,
	trustDecision trust.Trust,
	autoMergeLabel string,
	mechanicalOverrideAuthors []string,
) pkg.Watcher
```

   It builds a real `pkg.NewTaskPublisher` (same `TaskConfig` as the sibling helpers: `Stage: "dev"`, `MaxSlugLen: pkg.DefaultMaxSlugLen`, `MaxTitleLen: pkg.DefaultMaxTitleLen`, empty `TaskSuffix`) and a watcher with `taskCreationFilter` `{filter.NewDraftFilter(), filter.NewBotAuthorFilter([]string{"dependabot[bot]"})}`, `sideEffectFilter` `{filter.NewDraftFilter()}`, `trivialAutoMergeEnabled: false`, and `newDisabledUnreviewedScanner()`. `trivialAutoMergeEnabled` must be `false` so `maybeLabelTrivial` cannot confound these assertions — the feature is independent of that flag.

   Shared fixtures: a PR with `Number: 10`, `Owner: "bborbe"`, `Repo: "repo"`, `Title: "bump deps"`, `HTMLURL: "https://github.com/bborbe/repo/pull/10"`, `AuthorLogin: "ben-s-go-updater-dev[bot]"`, `IsDraft: false`, `UpdatedAt: libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))`; `GetPRDetailsReturns(pkg.PRDetails{HeadSHA: "deadbeef", CloneURL: "https://github.com/bborbe/repo.git", BaseRef: "master"}, nil)`; `createSender.SendCommandReturns(nil)`; a trust decision that trusts that login (`trust.NewAuthorAllowlist([]string{"ben-s-go-updater-dev[bot]"})`); `mechanicalOverrideAuthors` `[]string{"ben-s-go-updater-dev[bot]"}`; `autoMergeLabel` `"auto-merge"`; an opted-in config `maintainerconfig.MaintainerConfig{AutoMerge: maintainerconfig.AutoMergeConfig{Trivial: true}, PrReviewer: maintainerconfig.PrReviewerConfig{AutoApprove: true}}`; and the mechanical file fixture already used by the trivial auto-merge tests (`Filename: "go.mod"` with a `go 1.26.5` → `go 1.26.6` patch).

   Each entry asserts that exactly one command was published and that it is a review, not an override:

```go
	Expect(createSender.SendCommandCallCount()).To(Equal(1))
	_, cmd := createSender.SendCommandArgsForCall(0)
	Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
```

   Entries and their distinguishing setup / extra assertions:
   - `non-trivial files -> review only` — `ListPRFilesReturns(nonMechanical, nil)` where `nonMechanical` is the existing `pkg/watcher.go` patch fixture from `Describe("trivial auto-merge label")`. Extra: `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(1))` and `Expect(ghClient.ListPRFilesCallCount()).To(Equal(1))` — proves the gate ran all the way to the classifier.
   - `author not on mechanical list -> review only` — `mechanicalOverrideAuthors` `[]string{"someone-else[bot]"}`; trust still trusts the PR author. Extra: `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(0))` and `Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))`.
   - `author not trusted -> review only` — trust `trust.NewAuthorAllowlist([]string{"alice"})`; mechanical list still contains the PR author. Assert `task_type` is `pr-review` and `phase` is `human_review` (the untrusted routing), and `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(0))`.
   - `author case differs -> review only` — PR author `"Ben-S-Go-Updater-Dev[bot]"`, mechanical list `[]string{"ben-s-go-updater-dev[bot]"}`, and the trust allowlist containing the exact-case author `"Ben-S-Go-Updater-Dev[bot]"` so trust passes and only casing can reject. Extra: `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(0))`.
   - `autoMerge.trivial false -> review only` — config `maintainerconfig.MaintainerConfig{PrReviewer: maintainerconfig.PrReviewerConfig{AutoApprove: true}}`. Extra: `Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))`.
   - `prReviewer.autoApprove false -> review only` — config `maintainerconfig.MaintainerConfig{AutoMerge: maintainerconfig.AutoMergeConfig{Trivial: true}}`. Extra: `Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))`.
   - `draft -> no publication` — `IsDraft: true` on the PR. Assert `Expect(createSender.SendCommandCallCount()).To(Equal(0))`; this is the create-command count, which is exactly the "zero `PublishCreate` and zero `PublishOverride` calls" the spec names (a real publisher is used here, so a send is the only observable publication).
   - `config read fails -> review only` — `GetMaintainerConfigReturns(maintainerconfig.MaintainerConfig{}, errors.New("boom"))`. Assert one `pr-review` command and `Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))`.
   - `file listing fails -> review only` — opted-in config, `ListPRFilesReturns(nil, errors.New("boom"))`. Assert one `pr-review` command and `Expect(ghClient.ListPRFilesCallCount()).To(Equal(1))`.
   - `trust check fails -> no publication` — a trust decision of `trust.Func(func(ctx context.Context, _ trust.PR) (trust.Result, error) { return nil, errors.New("trust backend down") })`. Assert `Expect(createSender.SendCommandCallCount()).To(Equal(0))` (the review path's own trust check fails too, exactly as today).

   Add a second, sibling container so the spec's focused set stays exactly the ten entries above:

```go
	Describe("Mechanical override error paths", func() {
```

   with one entry `It("empty auto-merge label -> review only", ...)` — `autoMergeLabel: ""`, everything else qualifying. Assert one `pr-review` command and `Expect(ghClient.GetMaintainerConfigCallCount()).To(Equal(0))`. Do NOT put it inside the `Mechanical override for trivial bot bumps` container.

10. **Add parser tests** in `pkg/filter_test.go`, a `Describe("ParseMechanicalOverrideAuthors")` block mirroring `Describe("ParseTrustedAuthors")`: empty string → `BeNil()`; single login; multiple comma-separated with surrounding spaces; whitespace-only entry filtered; trailing comma filtered.

11. Before finishing, re-run `<verification>` and confirm it passes; then walk each numbered requirement above against the diff.
</requirements>

<constraints>
- **Three author lists, three meanings.** `BOT_ALLOWLIST` means "handled elsewhere, skip entirely"; `TRUSTED_AUTHORS` means "may receive a review and may have auto-merge armed"; `MECHANICAL_OVERRIDE_AUTHORS` means "may be approved without a review". Do not merge them, share a knob, or add a mechanical author to `BOT_ALLOWLIST` — the bot-author filter runs in `taskCreationFilter` before the gate, so a listed author there would make `processPR` return early and silently disable the feature.
- **Empty list is the off switch.** No separate enable flag is added.
- **Independent of `TRIVIAL_AUTO_MERGE`.** The gate reads the repo config and the file list itself; it must not depend on `maybeLabelTrivial` or `tryAutoMerge` having run.
- **Label precedence.** The override label is an explicit human action and wins over the mechanical gate; the gate is evaluated only after `tryOverride` has declined.
- **A GitHub API error on the config or file read means "does not qualify"** — the PR falls back to the normal review. A trust-check error keeps today's behavior: no publication either way.
- **No emission in this prompt.** No override command, no `override_reason`, no label write, no arming, no new metric label. A qualifying PR still gets a normal review. The next prompt in this series adds the emission.
- **Never merge.** Do not add a merge API call.
- **No new GitHub API calls for a PR whose author is not on the list** — the author and trust checks precede every read.
- Errors follow `github.com/bborbe/errors` only where an error is returned; this gate returns a bool and logs, matching `maybeLabelTrivial` / `tryAutoMerge`.
- Do not add a `time.Now()` call; this gate needs no clock.
- Do not run `go mod tidy`, `go get`, or `go mod vendor`.
- **Release ordering (operator-side, per stage).** Deploy `bborbe/github-pr-review-agent` PR #53 (which understands `override_reason: trivial-bot-bump`) before the watcher, and ship the `go.sum` classifier change in the same watcher release — without it `ClassifyTrivial` rejects every real bump and this feature stays dark. Do not change `pkg/trivial.go` here.
- Do not commit — dark-factory handles git.
- Existing tests must still pass, including every pre-existing entry in `pkg/watcher_test.go` and `pkg/filter_test.go`.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.

Then run the focused spec run and confirm the ten negative entries above are listed and 0 failures:

```
go test -mod=mod ./pkg/ -v -ginkgo.focus='Mechanical override'
```

Then confirm the configuration value is wired and the new parser exists:

```
grep -n 'MECHANICAL_OVERRIDE_AUTHORS' main.go
grep -n 'ParseMechanicalOverrideAuthors' pkg/filter.go main.go
```

And confirm no override emission leaked into this prompt (each command must exit 0, i.e. find nothing):

```
! grep -rq 'override_reason' pkg/ main.go
! grep -rq 'override_mechanical' pkg/ main.go
! grep -rq 'PublishMechanicalOverride' pkg/ main.go
```
</verification>
