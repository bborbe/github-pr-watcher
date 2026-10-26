---
status: completed
spec: [003-cask-trivial-classification]
summary: 'Verified and completed cask trivial classification: two line-shape regexps, Casks/*.rb routing, caskMechanical exact-pair rule, updated allow-list doc comment, eleven test entries, and changelog entry — precommit exits 0 and the focused Casks run reports Ran 11 of with 0 failures.'
execution_id: github-pr-watcher-same-repo-exec-010-spec-003-cask-trivial-classification
dark-factory-version: v0.198.7
created: "2026-10-24T09:19:10Z"
queued: "2026-10-24T09:36:55Z"
started: "2026-10-25T21:04:38Z"
completed: "2026-10-25T21:08:29Z"
---

# Classify a single-hash homebrew cask bump as a mechanical (trivial) change

<summary>
- A homebrew cask bump that changes only one `version` line and one `sha256` line is now recognised as a mechanical change.
- Homebrew cask files join the classifier's allow-list; every other file in that directory stays non-mechanical.
- A cask change is accepted only when exactly one `version` line and exactly one `sha256` line are removed and exactly one of each is added — the two always move together.
- A version change without a hash change, a hash change without a version change, a cross-kind swap, or an unpaired removal all still require a review.
- Multi-architecture casks (four hash lines) remain non-mechanical; that is a deliberate follow-up, not a variant of the accepted shape.
- Any other changed line in the file — a `url`, a `livecheck`, a `depends_on`, a new field — makes the whole change non-mechanical.
- A real, merged cask bump is captured as a test fixture, so the rule is proven against the actual GitHub patch shape rather than only synthetic input.
- The classifier's documented allow-list gains the cask case in the same change, so the comment never describes less than the code.
- No existing classification behaviour, no classifier signature, no caller and no existing test entry is changed or removed.
</summary>

<objective>
Teach `ClassifyTrivial` to accept a `Casks/<name>.rb` whose changes are exactly one paired `version` line and one paired `sha256` line, so a real single-hash cask bump classifies as trivial end to end, while every ambiguous cask shape still classifies as non-trivial.
</objective>

<context>
Read the project `docs/dod.md`.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before writing code:
- `pkg/trivial.go` — the `var (...)` regexp block, `ClassifyTrivial`, `isMechanicalFile`, `goSumMechanical`, `patchAll`, `appendOnly`. The new rule follows the same conventions: change lines are matched **with** their `+` / `-` prefix intact, diff scaffolding (`---`, `+++`, `@@`) is skipped, a context line (not starting with `+` or `-`) is ignored, and an empty patch is never mechanical.
- `pkg/trivial_test.go` — the `Describe("ClassifyTrivial")` / `DescribeTable` structure, `Entry` naming style (`"<shape> -> true|false"`), the patch-literal style, and the one existing entry that carries an origin comment above its file list.
- `pkg/githubclient.go` — `type PRFile struct { Filename string; Patch string; Additions int }`. Do not change it.
- `pkg/watcher.go` — the two call sites of `ClassifyTrivial` (around the trivial auto-merge path and the mechanical-override gate). Do not change them.

The spec for this change is `specs/in-progress/003-cask-trivial-classification.md`; its Acceptance Criteria name every test entry verbatim, and the names below match them exactly. The spec's Suggested Decomposition is one prompt: this one.
</context>

<requirements>
1. **Add the two line-shape regexps.** In the existing `var (...)` block of `pkg/trivial.go`, alongside `goModMechanical`, `dockerfileMechanical`, `workflowMechanical` and `goSumLine`, add:

```go
	// caskVersionLine matches one changed line of a single-hash homebrew cask:
	// a `version "..."` line, indented by the two spaces real cask lines carry
	// inside the `cask` block.
	caskVersionLine = regexp.MustCompile(`^[+-]  version "[^"]*"$`)
	// caskSHA256Line matches one changed `sha256 "<64 lowercase hex>"` line of a
	// single-hash homebrew cask, at the same two-space indent.
	caskSHA256Line = regexp.MustCompile(`^[+-]  sha256 "[0-9a-f]{64}"$`)
```

The two spaces between `[+-]` and the keyword are literal, not `\s{2}`: the accepted indent is exactly the two spaces a real cask line carries, and anything else is a shape we cannot prove. Uppercase hex is deliberately rejected. `regexp` is already imported. Do not alter the existing regexps.

2. **Route `Casks/<name>.rb` through a dedicated check.** In `isMechanicalFile`, add a case before the `default` arm:

```go
	case strings.HasPrefix(f.Filename, "Casks/") && strings.HasSuffix(f.Filename, ".rb"):
		return caskMechanical(f.Patch)
```

Leave the `go.mod`, `go.sum`, `Dockerfile`, `CHANGELOG.md` and workflow cases untouched. The `.rb` suffix is part of the contract: `Casks/README.md` must stay non-mechanical. Do **not** add any further path handling (no nested-directory rejection, no case folding) — the spec does not ask for it and an untested extra branch is worse than the simple prefix+suffix test.

3. **Implement `caskMechanical`** in `pkg/trivial.go`, immediately after `goSumMechanical` (before `patchAll`). This is the exact implementation:

```go
// caskMechanical returns true when a Casks/<name>.rb patch touches only one
// version line and one sha256 line, each removed line paired with an added
// line of the same kind. Pairing is by kind, never by count: a version
// addition cannot pay for a sha256 removal, and a multi-architecture bump
// (several sha256 lines) is not mechanical.
func caskMechanical(patch string) bool {
	if patch == "" {
		return false
	}
	var removedVersion, addedVersion, removedSHA256, addedSHA256 int
	for _, line := range strings.Split(patch, "\n") {
		if line == "" ||
			strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		switch {
		case caskVersionLine.MatchString(line):
			if line[0] == '-' {
				removedVersion++
			} else {
				addedVersion++
			}
		case caskSHA256Line.MatchString(line):
			if line[0] == '-' {
				removedSHA256++
			} else {
				addedSHA256++
			}
		default:
			return false
		}
	}
	return removedVersion == 1 && addedVersion == 1 &&
		removedSHA256 == 1 && addedSHA256 == 1
}
```

`strings` is already imported. Every branch above is exercised by the entries in requirement 5: empty patch, scaffolding skip, context skip, unknown-line rejection, and the final exact-count check.

4. **Update the `ClassifyTrivial` doc comment** so the allow-list it states matches the code. Change the sentence that begins "per the Mechanical Bump PR Pipeline allow-list:" so the list reads "go.mod toolchain/version bumps, go.sum hash bumps, Dockerfile base-image/GO_VERSION bumps, append-only CHANGELOG, workflow go-version bumps, and single-hash homebrew cask version/sha256 bumps". Change nothing else in the comment, and change nothing in the function body.

5. **Add the eleven test entries** to the existing `DescribeTable` in `pkg/trivial_test.go`, appended after the current last entry (`"dependency bump plus vendor change -> false"`) and before the closing `)` of the table. Every name below is load-bearing: the spec's focused run `-ginkgo.focus='Casks'` selects exactly these entries, and no pre-existing entry name contains `Casks`. Use these entries verbatim:

```go
		Entry(
			"Casks version+sha256 bump -> true",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"   desc \"Web UI for Vault\"\n" +
						"   homepage \"https://github.com/bborbe/vault-ui\"\n" +
						"-  version \"0.97.0\"\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"   name \"vault-ui\"\n",
				},
			},
			true,
		),
		Entry(
			"Casks version only -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  version \"0.97.0\"\n" +
						"+  version \"0.98.0\"\n" +
						"   sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks sha256 only -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"   version \"0.98.0\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks url change -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,8 +3,8 @@\n" +
						"-  version \"0.97.0\"\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"-  url \"https://example.com/vault-ui-0.97.0.tar.gz\"\n" +
						"+  url \"https://example.com/vault-ui-0.98.0.tar.gz\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks cross-kind pairing -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  version \"0.97.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks unpaired removal -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,5 @@\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks empty patch -> false",
			[]pkg.PRFile{{Filename: "Casks/vault-ui.rb", Patch: ""}},
			false,
		),
		Entry(
			"Casks multi-arch block bump -> false",
			// Synthetic: every hash sits at the two-space indent, so the exact
			// count rule (not the line shape) is what rejects the four hashes.
			[]pkg.PRFile{
				{
					Filename: "Casks/dark-factory.rb",
					Patch: "--- a/Casks/dark-factory.rb\n+++ b/Casks/dark-factory.rb\n@@ -1,10 +1,10 @@\n" +
						"-  version \"1.2.3\"\n" +
						"+  version \"1.2.4\"\n" +
						"-  sha256 \"0000000000000000000000000000000000000000000000000000000000000000\"\n" +
						"+  sha256 \"1111111111111111111111111111111111111111111111111111111111111111\"\n" +
						"-  sha256 \"2222222222222222222222222222222222222222222222222222222222222222\"\n" +
						"+  sha256 \"3333333333333333333333333333333333333333333333333333333333333333\"\n" +
						"-  sha256 \"4444444444444444444444444444444444444444444444444444444444444444\"\n" +
						"+  sha256 \"5555555555555555555555555555555555555555555555555555555555555555\"\n" +
						"-  sha256 \"6666666666666666666666666666666666666666666666666666666666666666\"\n" +
						"+  sha256 \"7777777777777777777777777777777777777777777777777777777777777777\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks non-ruby file -> false",
			// The patch is a shape the .rb branch would accept, so only the
			// filename-suffix check can make this false — a patch the cask rule
			// rejects on its own would let this entry pass vacuously.
			[]pkg.PRFile{
				{
					Filename: "Casks/README.md",
					Patch:    "--- a/Casks/README.md\n+++ b/Casks/README.md\n@@ -1,3 +1,3 @@\n-  version \"0.97.0\"\n-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n+  version \"0.98.0\"\n+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks real homebrew-tap bump -> true",
			// Origin: bborbe/homebrew-tap#29 — a vault-ui 0.98.0 bump, one file,
			// +2 -2. The patch below is the real hunk, pasted verbatim from
			// `gh -R bborbe/homebrew-tap pr diff 29`, including the real hashes,
			// the real hunk header, the comment context lines and the blank
			// context line before the url line.
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "@@ -3,8 +3,8 @@\n" +
						" # sha256 are bumped by .github/workflows/update-vault-ui-cask.yml when a new\n" +
						" # vault-ui release is published.\n" +
						" cask \"vault-ui\" do\n" +
						"-  version \"0.63.6\"\n" +
						"-  sha256 \"7435ca1d7bf4a461fb4732afe5c2e4d9ddfacdf611363cf0b8220faf7890885a\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"2c7a3399648d5bbeb9559bfc3bf54efd72f8cc4ae84574ca3da7977a7281841b\"\n" +
						" \n" +
						"   url \"https://github.com/bborbe/vault-ui/releases/download/v#{version}/vault_ui-#{version}-py3-none-any.whl\"\n",
				},
			},
			true,
		),
		Entry(
			"Casks add-cask PR with workflow and readme -> false",
			// Origin: the file set of bborbe/homebrew-tap#3 — three files: the new
			// cask, the updater workflow, and the README. The patches below are
			// minimal reproductions of each file's shape, not that PR's verbatim
			// text; the file set is what the entry proves. The cask file is an
			// all-added file, so it is non-mechanical on its own; the two siblings
			// make the PR non-mechanical regardless.
			[]pkg.PRFile{
				{
					Filename: "Casks/git-ai-sync.rb",
					Patch: "@@ -0,0 +1,6 @@\n" +
						"+cask \"git-ai-sync\" do\n" +
						"+  version \"0.1.0\"\n" +
						"+  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  url \"https://example.com/git-ai-sync-0.1.0.tar.gz\"\n" +
						"+  name \"git-ai-sync\"\n" +
						"+end\n",
				},
				{
					Filename: ".github/workflows/update-git-ai-sync-cask.yml",
					Patch: "@@ -0,0 +1,3 @@\n" +
						"+name: update cask\n" +
						"+on: workflow_dispatch\n" +
						"+jobs: {}\n",
				},
				{
					Filename: "README.md",
					Patch:    "--- a/README.md\n+++ b/README.md\n@@ -1,2 +1,3 @@\n # homebrew-tap\n+- git-ai-sync\n",
				},
			},
			false,
		),
```

Note the deliberate details in these fixtures: the context lines carry a leading space (`"   desc ..."` is space + two spaces + `desc`), so they are skipped by the same scaffolding/context rules `patchAll` uses. The real-bump entry deliberately has no `---` / `+++` file headers, mirroring how the GitHub files API returns a patch for one file. Do not "tidy" the indentation — the two-space indent is the shape under test.

6. **Add a changelog entry.** `CHANGELOG.md` already has a `## Unreleased` section. Append one bullet to it, below the existing bullets and above the `## v0.14.3` heading:

```markdown
- feat: classify a single-hash homebrew cask bump whose only changes are one paired `version` line and one paired `sha256` line as trivial, while multi-architecture casks and any other cask edit stay non-mechanical
```

7. **Do not change** `ClassifyTrivial`'s signature, its callers (`pkg/watcher.go`), `patchAll`, `appendOnly`, `goSumMechanical`, `PRFile`, or any pre-existing test entry. Do not touch the mechanical-override gate or the watcher's poll path.

8. **Self-check before finishing.** Re-run the whole `<verification>` block and confirm it passes, then walk each Acceptance Criterion 1-11 in `specs/in-progress/003-cask-trivial-classification.md` against the code and name the entry that proves it. Do not report done while any of the eleven entries is missing from the focused run.
</requirements>

<constraints>
- **Conservative direction.** A false positive ships an unreviewed behaviour change; a false negative costs one review. Every ambiguous cask shape must resolve to non-mechanical. Do not add leniency beyond the exact-count pairing rule stated above.
- **Exactly one pair per kind.** A multi-architecture bump (several `sha256` lines) is out of scope, not a variant of the accepted shape. Do not try to support it.
- **Paired by kind, not by count.** A `version` addition cannot pay for a `sha256` removal.
- **No formula support.** `.rb` files under `Formula/` are out of scope; the allow-list matches only the `Casks/` prefix.
- **The allow-list doc comment is part of the contract.** It gains the cask case in the same change.
- **Pure function.** `ClassifyTrivial` stays a pure function over `[]PRFile` — no I/O, no context, no config.
- **Patch-only.** A missing/empty patch is never mechanical.
- **Tests:** Ginkgo v2 / Gomega `DescribeTable` with one named `Entry` per shape, appended to the existing table in `pkg/trivial_test.go`. Every new entry name contains `Casks`; no pre-existing entry name does, so `-ginkgo.focus='Casks'` selects exactly the eleven.
- Do not delete, rename or weaken any existing `Entry`. The spec's negative check for that (`git diff … | grep -cE '^-'` prints `0`) is verified operator-side after the PR merges; do **not** run any `git` command from this prompt — inspect the test file instead. This repo's `.git` is masked in the container, so git output there is meaningless.
- Do not add, remove or bump dependencies. `make precommit` runs `ensure` (`go mod tidy` + `go mod verify` + `rm -rf vendor`) — that is expected; `go.mod` and `go.sum` must come out of it byte-identical. Never run `go get`, `go mod vendor`, or a `-mod=vendor` test.
- Do not commit — dark-factory handles git.
- Existing tests must still pass; `make precommit` must exit 0.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.

Then run the focused spec run. `-v` alone does not print Ginkgo spec names, so `-ginkgo.v` is required to list them, and `Ran 11 of` is the assertion — a focus that matches nothing still exits 0:

```
bash -o pipefail -c 'GOFLAGS=-buildvcs=false go test -mod=mod ./pkg/ -v -ginkgo.v -ginkgo.focus=Casks 2>&1 | tee /tmp/casks.log'
grep -q 'Ran 11 of' /tmp/casks.log
grep -q 'Casks version+sha256 bump -> true' /tmp/casks.log
grep -q 'Casks real homebrew-tap bump -> true' /tmp/casks.log
grep -q 'Casks multi-arch block bump -> false' /tmp/casks.log
```

Then confirm the classifier covers the file, in code and in the allow-list doc comment:

```
grep -q 'Casks/' pkg/trivial.go
grep -q 'single-hash homebrew cask version/sha256 bumps' pkg/trivial.go
```

Finally run the whole pre-existing classifier table to prove nothing was weakened:

```
GOFLAGS=-buildvcs=false go test -mod=mod ./pkg/ -v -ginkgo.focus='ClassifyTrivial'
```
</verification>
