---
status: verifying
approved: "2026-10-24T08:19:34Z"
generating: "2026-10-24T08:20:25Z"
prompted: "2026-10-24T08:25:48Z"
verifying: "2026-10-24T09:05:23Z"
branch: dark-factory/go-sum-trivial-classification
---

## Summary

- A dependency bump always rewrites `go.sum`, and the trivial classifier does not accept `go.sum`.
- So the classifier returns false for every real dependency bump — the case it was built for.
- `go.sum` becomes a mechanical file when every changed line is a module hash line and every removed line is paired with an added one for the same module.
- Nothing else about the classifier changes.

## Problem

`ClassifyTrivial` accepts `go.mod`, `Dockerfile`, `CHANGELOG.md` and workflow `go-version` bumps, but not `go.sum`. Every real dependency bump changes `go.mod` and `go.sum` together, so the classifier's verdict is false for the entire change class it exists to recognize. Both consumers are affected: the `autoMerge.trivial` labelling path never labels a real bump, and the mechanical override path (its own spec) could never fire.

## Reproduction

Reproduction is a pure-function call, not a pipeline run — `go test` with the inputs below reproduces it with no dark-factory involvement.

1. Take any merged dependency-bump PR, e.g. `bborbe/boltkv#29`: its changed files are exactly `CHANGELOG.md`, `go.mod`, `go.sum`.
2. Feed that file list to `ClassifyTrivial`.
3. It returns false, because `isMechanicalFile` has no `go.sum` case and its `default` arm returns false.

5/5 sampled bump PRs change exactly that file set, so the failure is not an edge case.

## Expected vs Actual

- **Expected:** a bump whose only changes are version and hash lines classifies as trivial, so `autoMerge.trivial` and the mechanical override can act on it. The allow-list contract is stated in `pkg/trivial.go`'s own doc comment: "go.mod toolchain/version bumps, Dockerfile base-image/GO_VERSION bumps, append-only CHANGELOG, and workflow go-version bumps" — a dependency bump belongs to that set.
- **Actual:** it classifies as non-trivial, so the opt-in silently never fires for the change class it was written for.

## Goal

`ClassifyTrivial` returns true for a real dependency bump and stays false for anything that is not one, including a `go.sum` that removes or rewrites a module's hash set in a way a version bump does not.

## Non-goals

- No change to the `go.mod`, `Dockerfile`, `CHANGELOG.md` or workflow patterns.
- No change to `ClassifyTrivial`'s signature or its callers.
- No new file types.
- No LLM or heuristic classification — the rule stays a pure string check.

## Assumptions

- GitHub returns a `go.sum` patch as a text unified diff. The existing `patchAll` helper matches change lines **with** their `+` / `-` prefix intact (every current pattern is anchored on `^[+-]`) and returns only a verdict, so the line-shape rule follows that convention while the pairing rule needs its own pass over the same patch.
- A truncated or binary file yields an empty patch, which the existing helper already rejects.
- A dependency bump is a *version* change of a module the build already uses: `go.mod` gains a new version for a module it lists, and `go.sum` gains the matching hash pair.

## Acceptance Criteria

Every entry name in ACs 1-10 below contains `go.sum`, so `go test ./pkg/ -v -ginkgo.focus='go.sum'` selects exactly this set and nothing else. Each AC is satisfied when that focused run lists the named entries and reports 0 failures.

- [ ] A `go.sum` whose removed hash line is paired with an added hash line for the same module path and the same `/go.mod` suffix presence classifies as trivial. Evidence: the entry `go.sum hash pair bump -> true`.
- [ ] A `go.sum` with any changed line that is not a module hash line classifies as non-trivial. Evidence: four entries — `go.sum line with a go directive -> false`, `go.sum bare module path -> false`, `go.sum h2 hash line -> false`, `go.sum comment -> false`.
- [ ] A removed hash line with no added line for the same module path and suffix classifies as non-trivial. Evidence: `go.sum unpaired removal -> false`.
- [ ] A hash rewritten at an **unchanged** version — the same `(path, version, /go.mod)` on both sides with a different hash — classifies as non-trivial: that replaces the pin `go.sum` exists to enforce, so it is not a version bump. Evidence: `go.sum same-version hash rewrite -> false`.
- [ ] A removal of one module's hash line paired with an addition for a *different* module path classifies as non-trivial, even when the added and removed counts are equal. Evidence: `go.sum cross-module swap -> false`.
- [ ] A removal of a `<module> v<version>/go.mod h1:…` line paired with an addition of `<module> v<version> h1:…` for the same module classifies as non-trivial — the `/go.mod` suffix presence must match. Evidence: `go.sum go.mod suffix mismatch -> false`.
- [ ] An empty patch on `go.sum` (a binary or truncated diff) classifies as non-trivial. Evidence: `go.sum empty patch -> false`.
- [ ] An addition with no matching removal — a newly required module — classifies as trivial, matching the `go.mod` pattern which already accepts an added `require` line. Evidence: `go.sum new module added -> true`.
- [ ] Pseudo-versions (`v0.0.0-20240101120000-abcdef123456`) and `+incompatible` versions classify as trivial, because they are version strings like any other. Evidence: `go.sum pseudo-version bump -> true` and `go.sum +incompatible bump -> true`.
- [ ] A three-file bump — `go.mod` with a paired module version change, `go.sum` with the matching paired hash change, and `CHANGELOG.md` with an appended line — classifies as trivial end to end. Evidence: `go.sum full dependency bump -> true`.
- [ ] The reproduction is replayed against a real bump, not only a synthetic one: the actual file set and patches of `bborbe/boltkv#29` (read with `gh -R bborbe/boltkv pr view 29 --json files`) are recorded as a fixture and classify as trivial. Evidence: `go.sum real boltkv bump -> true`, with the fixture's origin named in a comment above it.
- [ ] **Negative:** no pre-existing classifier entry was removed or weakened. Evidence: `git diff -U0 "$(git merge-base HEAD origin/master)" HEAD -- pkg/trivial_test.go | grep -cE '^-[^-]|^-$'` prints `0` (grep exits 1 at zero matches, which is the expected outcome here, not a failure), and `go test ./pkg/ -v -ginkgo.focus='ClassifyTrivial'` reports 0 failures across every entry that existed before this change.

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — fmt, generate, test, lint, vet, vuln, license
- `go test ./pkg/ -v -ginkgo.focus='go.sum'` — the new entries pass
- `grep -n 'go.sum' pkg/trivial.go` — the classifier covers the file

## Desired Behavior

1. `go.sum` joins the mechanical file allow-list.
2. Every changed line must be a module hash line of the form `<module> v<version> h1:<base64>=` or `<module> v<version>/go.mod h1:<base64>=`; a line that is not one makes the file non-mechanical.
3. Every removed hash line must be paired with an added line for the same module path and the same `/go.mod` suffix presence; an unpaired removal, or a pairing across two different modules, makes the file non-mechanical.
4. A real dependency bump (paired version changes in `go.mod` and `go.sum`, an appended `CHANGELOG.md` line) classifies as trivial end to end.

## Constraints

- **Conservative direction.** A false positive ships an unreviewed behavior change; a false negative costs one review. Every ambiguous `go.sum` shape resolves to non-mechanical.
- **Adding is in scope, dropping is not.** A bump that adds a hash pair for a new module classifies as trivial, mirroring the `go.mod` pattern. A bump that drops a module's hashes does not, because it changes the build graph.
- **Pure function.** `ClassifyTrivial` stays a pure function over `[]PRFile` — no I/O, no context, no config.
- **Patch-only.** The rule reads the unified diff, as the existing patterns do; a missing patch is never mechanical.
- **Tests:** Ginkgo v2 / Gomega `DescribeTable` with one named `Entry` per shape, in the existing `pkg/trivial_test.go` style.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| `go.sum` gains a line shape the rule does not know (a `h2:` hash, a new directive) | The file is non-mechanical, so the PR gets a review | Add the shape in a follow-up; the focused run then lists the new entry as passing |
| A dependency bump also runs `go mod tidy`, dropping several modules | Unpaired removals make the file non-mechanical, so the PR gets a review | Intended: a changed module set is not a version bump |
| A truncated or binary patch | Non-mechanical | None needed; the review runs |
| A vendor directory change accompanies the bump | `vendor/**` is not on the allow-list, so the PR is non-mechanical | Intended; a vendored change is content, not a version string |

## Security / Abuse

- The rule is the only control that decides what counts as mechanical, and it accepts version and hash lines only, in a fixed file allow-list. Nothing here reads credentials or writes outside the process.

## Do-Nothing Option

The classifier keeps returning false for every real dependency bump, so the `autoMerge.trivial` opt-in silently never fires for the case it was built for and the mechanical override path (its own spec) can never emit. The opt-in looks healthy on the config side while doing nothing.
