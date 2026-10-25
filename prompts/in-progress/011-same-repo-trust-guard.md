---
status: approved
created: "2026-10-25T20:22:56Z"
queued: "2026-10-25T20:33:22Z"
---

# Require a same-repo pull request before trusting it

<summary>
- A pull request is trusted only when its head repository is the same repository it targets, so a workflow running in a fork can no longer inherit trust from an author login it happens to share with the upstream bot
- Trust fails closed: when GitHub does not report the head or the base repository, the pull request is not trusted
- Every trust decision still carries a plain-language reason naming the repositories it compared, so a denial in the logs explains itself
- Every place the watcher asks "is this pull request trusted?" now supplies both repository names, so the new condition governs review publishing, override tasks, the mechanical-override gate, auto-merge arming, branch refresh, superseded dep-bump closing, the unreviewed-PR scan and the single-PR trigger
- The unreviewed-PR scan now reads the pull request's details before deciding trust, because the head repository only arrives with those details; a trusted pull request costs no extra GitHub call, while an untrusted author's pull request now costs one details fetch per scan it previously never made
- Operators get no new setting: the existing trusted-author list still applies and is now additionally bounded to same-repository pull requests
- Two existing tests change (the scanner's untrusted-author case for the new call order, and the `GetPRDetails` fake JSON for the two new fields) and one watcher test entry is added; no other test changes
</summary>

<objective>
Close the fork-PR trust hole. Trust currently rests on the author login alone, and a login is not an ownership proof — a workflow in a fork that opens a pull request against upstream renders as the same `github-actions[bot]` login, so trusting that login alone would let a fork pull request be auto-reviewed and auto-approved. Add a same-repo trust leaf, carry the head and base repository full names on both the trust input and the fetched pull-request details, and supply them at every trust call site. Consequence, intended and unoverridable: a fork pull request is denied on every path including an operator's explicit `/trigger`, which returns the non-retryable `ErrCommandObjectSkipped`.
</objective>

<context>
Read `docs/dod.md` before starting.

Read these files before changing anything:
- `pkg/trust/trust.go` — `PR`, `Result`, `NewResult(success bool, description string) Result`, `Trust`, `Func`, `And`.
- `pkg/trust/author_allowlist.go` — the structural exemplar for the new leaf: doc-comment shape, `Func` closure returning `(Result, error)`, one `NewResult` per outcome, fail-closed on empty input.
- `pkg/trust/author_allowlist_test.go` and `pkg/trust/suite_test.go` — the Ginkgo/Gomega shape (`package trust_test`, `Describe`/`It`, shared suite runner) the new test file follows.
- `pkg/githubclient.go` — the `PRDetails` struct and `GetPRDetails`; also the `GetPRDetails` Describe block in `pkg/githubclient_test.go`, whose fake JSON is extended in requirement 4.
- `pkg/watcher.go` — `(*taskPublisher).PublishCreate`, `(*taskPublisher).PublishOverride`, `(*watcher).tryUpdateBranch`, `(*watcher).trySupersedeDepBump`, `(*watcher).tryAutoMerge`, `(*watcher).qualifiesForMechanicalOverride`, `(*watcher).tryMechanicalOverride`, `(*watcher).processPR`, `(*watcher).processLabeledPRs`, and `(*watcher).fetchPRDetails` (the per-poll `PRDetails` cache the new fields flow through unchanged).
- `pkg/unreviewed_scanner.go` — `(*unreviewedScanner).qualify` and its `cachedReviewed` helper.
- `pkg/command/trigger_pr_review_executor.go` — `applyTrust`.
- `main.go` — `trustDecision := trust.And{trust.NewAuthorAllowlist(trustedAuthors)}`.

Pattern docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-boolean-combinator-pattern.md` — the contract this package implements: a decision returns a `Result` carrying both the outcome and a human-readable `Description()`, never a naked bool, and every leaf answers "why" for both outcomes.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
</context>

<requirements>
1. **Two repository fields on the trust input.** In `pkg/trust/trust.go`, extend `PR` with two string fields, each carrying a GoDoc comment, placed after `AuthorLogin`:

```go
type PR struct {
	AuthorLogin string

	// HeadRepoFullName is the head repository's `owner/repo` full name as
	// GitHub reports it (`pr.head.repo.full_name`). Empty when the head repo
	// is unknown — a deleted fork, or a PR derived from the Search API, which
	// does not carry it.
	HeadRepoFullName string

	// BaseRepoFullName is the base repository's `owner/repo` full name
	// (`pr.base.repo.full_name`). Empty when unknown.
	BaseRepoFullName string
}
```

2. **The same-repo leaf.** Create `pkg/trust/same_repo.go` in `package trust`, following `pkg/trust/author_allowlist.go` structurally (license header, `Func` closure, `NewResult` per outcome, no error path — a comparison cannot fail, so always return a nil error). `func NewSameRepo() Trust` takes no arguments. Its doc comment states the threat it closes: an author login is not an ownership proof, because a workflow running in a fork that opens a PR against upstream renders as the same login, so trusting that login alone would let a fork PR be auto-reviewed. Decision order and the required description content:

| condition | outcome | description must contain |
|---|---|---|
| `pr.HeadRepoFullName == ""` | deny | the word `head` |
| `pr.BaseRepoFullName == ""` | deny | the word `base` |
| both present, equal by exact byte comparison | trust | both values |
| both present, different | deny | both values |

   Both empty checks come before the comparison — an empty value must never compare equal to another empty value and grant trust. Reference body:

```go
func NewSameRepo() Trust {
	return Func(func(_ context.Context, pr PR) (Result, error) {
		if pr.HeadRepoFullName == "" {
			return NewResult(false, "head repo unknown (no head repo full name provided)"), nil
		}
		if pr.BaseRepoFullName == "" {
			return NewResult(false, "base repo unknown (no base repo full name provided)"), nil
		}
		if pr.HeadRepoFullName == pr.BaseRepoFullName {
			return NewResult(
				true,
				fmt.Sprintf("head repo %q is the base repo", pr.HeadRepoFullName),
			), nil
		}
		return NewResult(
			false,
			fmt.Sprintf(
				"head repo %q is not the base repo %q",
				pr.HeadRepoFullName,
				pr.BaseRepoFullName,
			),
		), nil
	})
}
```

   Comparison is exact byte equality — no case folding, no trimming, matching `NewAuthorAllowlist`'s handling of logins.

3. **Two repository fields on the fetched details.** In `pkg/githubclient.go`, add `HeadRepoFullName string` and `BaseRepoFullName string` to `PRDetails`, each with a GoDoc comment, placed after `HeadRef` (the struct keeps its repo/branch identity group together). Document on `HeadRepoFullName` that it is empty when GitHub no longer reports a head repo (a deleted fork) and that it feeds the same-repo trust check.

4. **Populate them in `GetPRDetails` only.** In `GetPRDetails`, add two entries to the returned `PRDetails` literal, after `HeadRef`:

```go
		HeadRepoFullName: pr.GetHead().GetRepo().GetFullName(),
		BaseRepoFullName: pr.GetBase().GetRepo().GetFullName(),
```

   Verified signatures in `github.com/google/go-github/v62` (already imported): `func (p *PullRequestBranch) GetRepo() *Repository` and `func (r *Repository) GetFullName() string`.

   Do NOT attempt to populate anything on `PullRequest`: it is built from the GitHub Search API (`Search.Issues`), which carries no head repo, so no field exists to fill there.

   Extend the existing `Describe("GetPRDetails")` test in `pkg/githubclient_test.go`: add `"full_name": "forkowner/repo"` to the `head.repo` object and `"repo": {"full_name": "owner/repo"}` alongside the existing `"ref": "master"` in the `base` object of the fake JSON, then assert `details.HeadRepoFullName` equals `"forkowner/repo"` and `details.BaseRepoFullName` equals `"owner/repo"`. The two values must differ — with one shared value a head/base swap in the `PRDetails` literal would still pass. This test is the boundary check that the JSON decode path actually reaches the new fields; a struct-shape assertion alone would not catch a wrong JSON key. Leave the HTTP-failure test untouched, and do not change the existing `clone_url` value or the existing `CloneURL` assertion — `head.repo.full_name` deliberately differs from it so the two new assertions cannot both pass on a head/base swap.

5. **Thread the fields to every trust call site.** There are eight `trust.PR{` constructions outside the trust package's own tests. All of them gain both fields from a `details PRDetails` value that is already in scope, using this shape (shape only — keep each site's own receiver and author expression from the table below; `PublishCreate` uses `p.trustDecision` and `author`):

```go
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
```

   Keep each site's existing `AuthorLogin` expression and its existing error/deny handling unchanged:

| file | function | current `AuthorLogin` expression | `details` source |
|---|---|---|---|
| `pkg/watcher.go` | `(*taskPublisher).PublishCreate` | `author` (local `author := pr.AuthorLogin`) | parameter |
| `pkg/watcher.go` | `(*taskPublisher).PublishOverride` | `pr.AuthorLogin` | parameter |
| `pkg/watcher.go` | `(*watcher).tryUpdateBranch` | `pr.AuthorLogin` | parameter |
| `pkg/watcher.go` | `(*watcher).trySupersedeDepBump` | `pr.AuthorLogin` | parameter |
| `pkg/watcher.go` | `(*watcher).tryAutoMerge` | `pr.AuthorLogin` | **new parameter — see below** |
| `pkg/watcher.go` | `(*watcher).qualifiesForMechanicalOverride` | `pr.AuthorLogin` | **new parameter — see below** |
| `pkg/unreviewed_scanner.go` | `(*unreviewedScanner).qualify` | `pr.AuthorLogin` | local `details` — see requirement 6 |
| `pkg/command/trigger_pr_review_executor.go` | `applyTrust` | `details.AuthorLogin` | parameter |

   Two functions lack `details` and need a `details PRDetails` parameter added immediately after `pr PullRequest`:

```go
func (w *watcher) tryAutoMerge(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
)
```

```go
func (w *watcher) qualifiesForMechanicalOverride(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) bool
```

   Update every caller of both — all three `tryAutoMerge` call sites already hold a `details` value, and so does the single `qualifiesForMechanicalOverride` call site:
   - `(*watcher).processLabeledPRs`, inside the labeled-PR loop, right after `w.tryUpdateBranch(ctx, pr, details)`.
   - `(*watcher).processPR`, right after `w.tryUpdateBranch(ctx, pr, details)`.
   - `(*watcher).tryMechanicalOverride`, right after `pr.Labels = append(pr.Labels, w.autoMergeLabel)`.
   - `(*watcher).tryMechanicalOverride`, the `qualifiesForMechanicalOverride` gate.

   All four become the `(ctx, pr, details)` form. `details` is already a parameter of `processPR`, `processLabeledPRs`'s loop body and `tryMechanicalOverride`, so no other plumbing is needed.

6. **Move the scanner's trust check after its details fetch.** In `(*unreviewedScanner).qualify`, the trust check currently runs before `s.ghClient.GetPRDetails(...)`, where the head repo is not yet known — leaving it there would deny every pull request once the new leaf is registered. Delete the trust block from its current position (keep the `s.taskCreationFilter.Skip` gate, the override-label gate, the `now.Sub(pr.UpdatedAt) < s.cfg.Threshold` wait and the `s.cachedReviewed(pr)` shortcut exactly where they are — none of them costs a GitHub call — note this puts `cachedReviewed` ahead of the trust check, which is intended: only a PR already confirmed reviewed at its current `UpdatedAt` is cached, so it passed the check when it was recorded), and insert it directly after the `GetPRDetails` error block, before the `details.HeadSHA == ""` fail-closed check:

```go
	details, err := s.ghClient.GetPRDetails(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return PRDetails{}, false, err
	}
	trustResult, err := s.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		return PRDetails{}, false, errors.Wrapf(ctx, err, "trust check %s", prKey(pr))
	}
	if !trustResult.Success() {
		return PRDetails{}, false, nil
	}
	// Fail-closed, mirroring processPR: no head SHA means no reviewable commit.
	if details.HeadSHA == "" {
		return PRDetails{}, false, nil
	}
```

   For a trusted author this adds no GitHub call: `GetPRDetails` was already made for every PR that reaches this point. For an untrusted author it adds one — trust now runs after the fetch, so a PR that previously returned before `GetPRDetails` now fetches it on every scan, since `cachedReviewed` never caches an untrusted PR. A `GetPRDetails` error on such a PR now also aborts the scan. Both are accepted costs of moving the check; say so in a code comment so a later reader does not move the fetch back above the trust check.

   One existing test encodes the old order — `It("skips an untrusted author without fetching details")` in `pkg/unreviewed_scanner_test.go`. Rename it to reflect the new behaviour (an untrusted author is skipped after the details fetch, still with no review lookup) and change `Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))` to `To(Equal(1))`, keeping the empty-gauge assertion and adding `Expect(ghClient.ListReviewsCallCount()).To(Equal(0))`. The other early-exit tests (draft, repo-allowlist, override label) still return before the fetch and keep their `To(Equal(0))` expectations — do not change them. `It("aborts the scan when a trust check errors")` keeps passing unchanged, because a trust error still aborts the scan.

7. **Register the leaf.** In `main.go`, the composition becomes:

```go
	trustDecision := trust.And{trust.NewAuthorAllowlist(trustedAuthors), trust.NewSameRepo()}
```

   The one `trustDecision` value is shared by the watcher, the command consumer and the single-PR trigger executor, so all three paths pick the new condition up from this single site.

8. **Unit tests for the leaf.** Create `pkg/trust/same_repo_test.go` (`package trust_test`), following `pkg/trust/author_allowlist_test.go`; it runs under the existing `pkg/trust/suite_test.go` runner, so do not add a second `TestSuite`. Cover, one `It` each:
   - same head and base repo → `Success()` true, description contains the repo name;
   - different head and base repo → `Success()` false, description contains **both** repo names (the audit trail must name what it compared);
   - empty `HeadRepoFullName` → `Success()` false, description contains `head`;
   - empty `BaseRepoFullName` → `Success()` false, description contains `base`;
   - both fields empty → `Success()` false (never a vacuous same-repo match);
   - the production composition — `trust.And{trust.NewAuthorAllowlist([]string{"alice"}), trust.NewSameRepo()}` — trusts a same-repo pull request from a trusted author and denies a fork pull request whose author is on that same allowlist, so registering the leaf demonstrably narrows the chain the watcher, the command consumer and the single-PR trigger executor share;
   - description non-empty for every outcome, asserted in a loop over the inputs above, mirroring the existing `"description is non-empty for all outcomes"` test.

   Every assertion checks `err` is nil, since the leaf has no error path. Case sensitivity is not a scenario to test — the fields are GitHub-format `owner/repo` values compared byte-exactly, and a case-folded variant would be asserting behaviour the leaf deliberately does not have.

9. **Self-check before finishing.** Re-run `<verification>` and confirm it passes, then walk requirements 1–10 against the diff: two fields on `PR`, two on `PRDetails`, two populated in `GetPRDetails`, eight `trust.PR{` literals carrying the repo names, both new parameters wired at all four call sites, the scanner's trust check after its fetch, and the leaf in the `And`.

10. **One trust test at a real watcher call site.** Add a single entry to `pkg/watcher_test.go` proving the repo names actually reach the leaf through a production path — the other seven sites are wired identically, so one is the sample. Give it its own chain and its own `PRDetails` fixture so no existing entry changes:

```go
trustDecision := trust.And{
	trust.NewAuthorAllowlist([]string{"alice"}),
	trust.NewSameRepo(),
}
```

   - `tryAutoMerge` with `HeadRepoFullName: "forkowner/repo"` and `BaseRepoFullName: "owner/repo"` → `EnableAutoMergeCallCount()` is 0 and the `auto_merge_skipped` counter moved;
   - as its own `It` with a fresh watcher and cursor, both fields set to `"owner/repo"` → `EnableAutoMergeCallCount()` is 1. It must be a separate `It`: `PublishCreate` returns true even for an untrusted author, so a second poll inside the first `It` would find the SHA already in the cursor and skip the PR.

   Without this, dropping the two fields at one of the eight sites compiles clean and silently trusts a fork pull request on that path. Requirement 9's diff walk is not a test.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Errors via `github.com/bborbe/errors`; no `fmt.Errorf`; no bare `return err` where a wrap can add context. The new leaf returns a nil error on every path, so this applies to the code you touch around it, not to the leaf.
- Every new `.go` file carries the license header used across the repo (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` plus the BSD two-line notice).
- Existing tests must still pass. The only existing tests you change are the untrusted-author case in `pkg/unreviewed_scanner_test.go` (requirement 6) and the extended `GetPRDetails` JSON assertions (requirement 4). `pkg/watcher_test.go` composes its own trust chains from `trust.NewAuthorAllowlist` alone and its fixtures leave the new fields empty, so none of its existing entries change — do not add `NewSameRepo()` to those chains and do not populate the new fields in its existing `PRDetails` fixtures. The one new entry required by requirement 10 brings its own chain and its own fixture.
- Do not add a configuration flag, environment variable, environment-parsed field, or metric that relaxes, disables or counts this check. The trusted-author list is the only operator-facing knob and it stays as it is.
- Do not touch `PullRequest` or the Search API call sites: the search response carries no head repo.
- Do not edit `README.md` or `CHANGELOG.md` — dark-factory owns release notes here. Note that this leaves the README's trust descriptions login-only; the follow-up doc change is out of scope for this prompt.
- Do not run `go mod vendor`, `go get`, or bump dependencies; do not hand-edit `go.mod` / `go.sum`.
</constraints>

<verification>
```
ROOTDIR=/workspace make precommit
```
Must pass with exit code 0 — it runs `go mod tidy`, formatting, mock regeneration, the full test suite, lint, vet, vulnerability and license-header checks.
</verification>
