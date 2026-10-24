---
status: completed
spec: [001-go-sum-trivial-classification]
summary: Taught ClassifyTrivial to accept go.sum patches whose changed lines are only paired h1 hash entries (with a go.mod-suffix-aware pairing rule), added 14 Ginkgo entries including the real bborbe/boltkv#29 fixture, and recorded an Unreleased changelog entry.
execution_id: github-pr-watcher-mechanical-approve-exec-006-go-sum-trivial-classification
dark-factory-version: v0.198.7
created: "2026-10-24T08:21:40Z"
queued: "2026-10-24T09:01:20Z"
started: "2026-10-24T09:02:03Z"
completed: "2026-10-24T09:05:23Z"
---

# Classify a go.sum hash bump as a mechanical (trivial) change

<summary>
- A dependency bump that touches only the module file, the module hash file and an appended changelog line is now recognised as a mechanical change.
- The classifier accepts the module hash file, so real dependency-bump pull requests stop being rejected by the trivial-auto-merge opt-in.
- Every changed line in that file must look like a module hash entry; a directive, a comment, a bare module path or a different hash algorithm makes the change non-mechanical.
- A hash entry that disappears must be replaced by an added entry for the same module and the same `/go.mod` suffix presence, so dropping a module or swapping two modules still needs a review.
- A brand-new module appearing with no removal is accepted, matching how the module file already treats a newly added requirement.
- Pseudo-versions and `+incompatible` versions are accepted like any other version string.
- A real, merged dependency-bump pull request is captured as a test fixture, so the fix is proven against actual GitHub data and not only synthetic input.
- No existing classification behaviour, no classifier signature, no caller and no existing test entry is changed or removed.
</summary>

<objective>
Teach `ClassifyTrivial` to accept a `go.sum` whose changes are only module hash lines forming a version bump, so a real dependency bump (`go.mod` + `go.sum` + appended `CHANGELOG.md`) classifies as trivial end to end, while every ambiguous `go.sum` shape still classifies as non-trivial.
</objective>

<context>
Read the project `docs/dod.md`, and these coding-plugin docs (paths as they exist inside the container):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/definition-of-done.md`

Read these source files fully before writing code:
- `pkg/trivial.go` — the `var (...)` regexp block, `ClassifyTrivial`, `isMechanicalFile`, `patchAll`, `appendOnly`. The new rule must follow the same conventions: change lines are matched **with** their `+` / `-` prefix intact, diff scaffolding (`---`, `+++`, `@@`) is skipped, a context line (not starting with `+` or `-`) is ignored, and an empty patch is never mechanical.
- `pkg/trivial_test.go` — the `Describe("ClassifyTrivial")` / `DescribeTable` structure, `Entry` naming style (`"<shape> -> true|false"`), and the patch-literal style. New entries are appended to the same table.
- `pkg/githubclient.go` — `type PRFile struct { Filename string; Patch string; Additions int }`. Do not change it.
- `pkg/watcher.go` — the single call site `if !ClassifyTrivial(files)` inside the trivial auto-merge path. Do not change it.

The spec for this change is `specs/in-progress/001-go-sum-trivial-classification.md`; its Acceptance Criteria name every test entry verbatim, and the names below match them exactly.
</context>

<requirements>
1. **Add the line-shape regexp.** In the existing `var (...)` block of `pkg/trivial.go`, alongside `goModMechanical`, `dockerfileMechanical` and `workflowMechanical`, add:

```go
	// goSumLine matches one changed go.sum line: a module path, a version
	// (optionally carrying the `/go.mod` suffix), and an h1 hash.
	goSumLine = regexp.MustCompile(`^[+-]\S+ v\S+ h1:[A-Za-z0-9+/]+={0,2}$`)
```

`regexp` is already imported. Do not alter the three existing regexps.

2. **Route `go.sum` through a dedicated check.** In `isMechanicalFile`, add a case before the `default` arm:

```go
	case f.Filename == "go.sum":
		return goSumMechanical(f.Patch)
```

Leave the `go.mod`, `Dockerfile`, `CHANGELOG.md` and workflow cases untouched.

3. **Implement `goSumMechanical`** in `pkg/trivial.go`, immediately after `isMechanicalFile` (before `patchAll`). This is the exact implementation:

```go
// goSumMechanical returns true when a go.sum patch touches only module hash
// lines and every removed hash line is paired with an added hash line for the
// same module path and the same /go.mod suffix presence. An unpaired addition
// (a newly required module) is accepted; an unpaired removal is not, because
// it changes the build graph.
func goSumMechanical(patch string) bool {
	if patch == "" {
		return false
	}
	type moduleKey struct {
		path  string
		goMod bool
	}
	removed := map[moduleKey]int{}
	added := map[moduleKey]int{}
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
		if !goSumLine.MatchString(line) {
			return false
		}
		fields := strings.Fields(line[1:])
		key := moduleKey{
			path:  fields[0],
			goMod: strings.HasSuffix(fields[1], "/go.mod"),
		}
		if line[0] == '-' {
			removed[key]++
		} else {
			added[key]++
		}
	}
	for key, count := range removed {
		if added[key] < count {
			return false
		}
	}
	return true
}
```

`strings` is already imported. The pairing key is the module path plus the `/go.mod` suffix presence — deliberately **not** the version, because a version bump is exactly the case where the version changes. Additions may outnumber removals (a new module); removals may never outnumber additions for the same key.

4. **Update the `ClassifyTrivial` doc comment** so the allow-list it states matches the code. Change the sentence that begins "per the Mechanical Bump PR Pipeline allow-list:" to include go.sum hash bumps, e.g. "go.mod toolchain/version bumps, go.sum hash bumps, Dockerfile base-image/GO_VERSION bumps, append-only CHANGELOG, and workflow go-version bumps." Change nothing else in the comment, and change nothing in the function body.

5. **Add the test entries** to the existing `DescribeTable` in `pkg/trivial_test.go`, appended after the current last entry (`"empty patch (large/binary) -> false"`) and before the closing `)` of the table. Every name below is load-bearing: the spec's focused run `-ginkgo.focus='go.sum'` selects exactly these entries, and no pre-existing entry name contains `go.sum`. Use these entries verbatim:

```go
		Entry(
			"go.sum hash pair bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch: "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n github.com/other/mod v1.0.0 h1:abc=\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum line with a go directive -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+go 1.26.6\n",
				},
			},
			false,
		),
		Entry(
			"go.sum bare module path -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+github.com/foo/bar\n",
				},
			},
			false,
		),
		Entry(
			"go.sum h2 hash line -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+github.com/foo/bar v1.2.4 h2:abc=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum comment -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+# comment\n",
				},
			},
			false,
		),
		Entry(
			"go.sum unpaired removal -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,2 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum cross-module swap -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/baz v1.2.3 h1:def=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum go.mod suffix mismatch -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3/go.mod h1:abc=\n+github.com/foo/bar v1.2.3 h1:def=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum empty patch -> false",
			[]pkg.PRFile{{Filename: "go.sum", Patch: ""}},
			false,
		),
		Entry(
			"go.sum new module added -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,4 @@\n+github.com/new/mod v1.0.0 h1:abc=\n+github.com/new/mod v1.0.0/go.mod h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum pseudo-version bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v0.0.0-20231231235959-abcdef123456 h1:abc=\n+github.com/foo/bar v0.0.0-20240101120000-abcdef123456 h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum +incompatible bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.9.0 h1:abc=\n+github.com/foo/bar v2.0.0+incompatible h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum full dependency bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo/bar v1.2.3\n+\tgithub.com/foo/bar v1.2.4\n",
				},
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
				{
					Filename: "CHANGELOG.md",
					Patch:    "--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,3 +1,4 @@\n ## Unreleased\n+\n+- chore: bump dependency\n",
				},
			},
			true,
		),
		Entry(
			"go.sum real boltkv bump -> true",
			// Origin: bborbe/boltkv#29 — the CHANGELOG.md, go.mod and go.sum
			// patches exactly as returned by
			// `gh -R bborbe/boltkv pr view 29 --json files`.
			[]pkg.PRFile{
				{
					Filename: "CHANGELOG.md",
					Patch: "@@ -8,6 +8,10 @@ Please choose versions by [Semantic Versioning](http://semver.org/).\n" +
						" * MINOR version when you add functionality in a backwards-compatible manner, and\n" +
						" * PATCH version when you make backwards-compatible bug fixes.\n" +
						" \n" +
						"+## Unreleased\n" +
						"+\n" +
						"+- chore: update go module dependencies\n" +
						"+\n" +
						" ## v1.16.3\n" +
						" \n" +
						" - chore: update Go to 1.27.3 and github.com/bborbe/errors to v1.6.4, github.com/bborbe/kv to v1.21.19, github.com/bborbe/sentry to v1.10.6, github.com/bborbe/service to v1.11.1, github.com/onsi/ginkgo/v2 to v2.33.1, github.com/onsi/gomega to v1.44.0",
				},
				{
					Filename: "go.mod",
					Patch: "@@ -16,7 +16,7 @@ require (\n" +
						" require (\n" +
						" \tgithub.com/Masterminds/semver/v3 v3.5.0 // indirect\n" +
						" \tgithub.com/bborbe/argument/v2 v2.13.8 // indirect\n" +
						"-\tgithub.com/bborbe/collection v1.21.3 // indirect\n" +
						"+\tgithub.com/bborbe/collection v1.21.4 // indirect\n" +
						" \tgithub.com/bborbe/math v1.4.16 // indirect\n" +
						" \tgithub.com/bborbe/parse v1.12.4 // indirect\n" +
						" \tgithub.com/bborbe/run v1.11.3 // indirect",
				},
				{
					Filename: "go.sum",
					Patch: "@@ -2,8 +2,8 @@ github.com/Masterminds/semver/v3 v3.5.0 h1:kQceYJfbupGfZOKZQg0kou0DgAKhzDg2NZPAw\n" +
						" github.com/Masterminds/semver/v3 v3.5.0/go.mod h1:4V+yj/TJE1HU9XfppCwVMZq3I84lprf4nC11bSS5beM=\n" +
						" github.com/bborbe/argument/v2 v2.13.8 h1:iAhOA67f3NMnOI0SwnaPT5+HW99vHA8whjORfDYmbtU=\n" +
						" github.com/bborbe/argument/v2 v2.13.8/go.mod h1:s2AypULcQok4aVLBTcJTFJ+0nemtVkrxwo2XFOgOSyo=\n" +
						"-github.com/bborbe/collection v1.21.3 h1:fAnKKYLGsBca/NNTuHtSgYIVad3Yw52Q9Krr/xCRO2k=\n" +
						"-github.com/bborbe/collection v1.21.3/go.mod h1:7N1XFVM4H41Ou6VaknELQHyEtfD5wly0lGz8LkqzJdU=\n" +
						"+github.com/bborbe/collection v1.21.4 h1:eyxNnsdbsxojlT9eDlGGclrW/vg8v+43/t0LVCUSZ8A=\n" +
						"+github.com/bborbe/collection v1.21.4/go.mod h1:k+qS5lxi4qQ/wpw8LiSBr08RiybIDc5/mbzs9yKDq0w=\n" +
						" github.com/bborbe/errors v1.6.4 h1:gPOl00mChbQ5WX9tqwA2zYRNjGFD32xBuyBV0Z47CD4=\n" +
						" github.com/bborbe/errors v1.6.4/go.mod h1:AjaF51Xl+xFEtNQyfSx6KkLR4qQkcM/6qi8xYaNjgEU=\n" +
						" github.com/bborbe/kv v1.21.19 h1:qgcm1QH4ATm4woOrKX6A007p5BVR4wm/KoFBMvoGvu8=",
				},
			},
			true,
		),
		Entry(
			"dependency bump plus vendor change -> false",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo/bar v1.2.3\n+\tgithub.com/foo/bar v1.2.4\n",
				},
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
				{
					Filename: "vendor/modules.txt",
					Patch:    "--- a/vendor/modules.txt\n+++ b/vendor/modules.txt\n@@ -1,3 +1,3 @@\n-# github.com/foo/bar v1.2.3\n+# github.com/foo/bar v1.2.4\n",
				},
			},
			false,
		),
```

Note the deliberate details in this fixture: the `CHANGELOG.md` patch is append-only **only because** the final `- chore: update Go …` line is a context line carrying its leading space — keep that leading space. The `go.mod` context lines carry a leading space followed by a tab. The `go.sum` fixture is the real hunk from the merged PR.

6. **Add a changelog entry.** `CHANGELOG.md` has no `## Unreleased` section today. Insert one immediately after the preamble — directly below the sentence ending "and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)." and above the `## v0.14.3` heading — with exactly one bullet:

```markdown
## Unreleased

- fix: classify a dependency bump whose only changes are paired go.sum hash lines, go.mod version lines and an appended CHANGELOG line as trivial
```

7. **Do not change** `ClassifyTrivial`'s signature, its callers (`pkg/watcher.go`), `patchAll`, `appendOnly`, `PRFile`, or any pre-existing test entry. The new function must be fully exercised by the entries above — every branch (empty patch, scaffolding skip, context skip, shape rejection, removal accounting, addition accounting, unpaired-removal rejection, final true) has an entry.

8. **Self-check before finishing.** Re-run the whole `<verification>` block and confirm it passes, then walk each Acceptance Criterion in `specs/in-progress/001-go-sum-trivial-classification.md` against the code and name the entry that proves it. Do not report done while any of the 14 entries is missing from the focused run.
</requirements>

<constraints>
- **Conservative direction.** A false positive ships an unreviewed behaviour change; a false negative costs one review. Every ambiguous `go.sum` shape must resolve to non-mechanical. Do not add leniency beyond the pairing rule stated above.
- **Adding is in scope, dropping is not.** An unpaired addition (newly required module) is accepted; an unpaired removal is not.
- **Pure function.** `ClassifyTrivial` stays a pure function over `[]PRFile` — no I/O, no context, no config.
- **Patch-only.** A missing/empty patch is never mechanical. No new file types beyond `go.sum`.
- No LLM or heuristic classification — the rule stays a pure string check.
- Do not delete, rename or weaken any existing `Entry` in `pkg/trivial_test.go`. The spec's negative check (`git diff … | grep -cE '^-'` prints `0`) is verified operator-side after the PR merges; do **not** run any `git` command from this prompt — inspect the test file instead.
- Do not add, remove or bump dependencies. `make precommit` runs `ensure` (`go mod tidy` + `go mod verify` + `rm -rf vendor`) — that is expected; `go.mod` and `go.sum` must come out of it byte-identical. Never run `go get`, `go mod vendor`, or a `-mod=vendor` test.
- Do not commit — dark-factory handles git.
- Existing tests must still pass; `make precommit` must exit 0.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must pass with exit code 0.

Then run the focused spec run. `-v` alone does not print Ginkgo spec names (see `go-testing-guide.md` § "Verbose Output"), so `-ginkgo.v` is required to list them, and `Ran 14 of` is the assertion — a focus that matches nothing still exits 0:

```
set -o pipefail
go test -mod=mod ./pkg/ -v -ginkgo.v -ginkgo.focus='go.sum' 2>&1 | tee /tmp/gosum.log
grep -q 'Ran 14 of' /tmp/gosum.log
grep -q 'go.sum real boltkv bump -> true' /tmp/gosum.log
```

Then confirm the classifier covers the file:

```
grep -n 'goSumMechanical' pkg/trivial.go
```

Finally run the whole pre-existing classifier table to prove nothing was weakened:

```
go test -mod=mod ./pkg/ -v -ginkgo.focus='ClassifyTrivial'
```
</verification>
