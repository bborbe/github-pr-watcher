---
status: draft
created: 2026-10-24
---

## Summary

- A single-hash homebrew cask bump changes exactly two lines: the `version` and its `sha256`.
- The trivial classifier does not know cask files, so every such bump gets a full LLM review.
- A cask file becomes mechanical when exactly one `version` line and exactly one `sha256` line change, in pairs.
- A version change without a matching sha256 change is rejected — the two always move together.
- Multi-architecture casks (four `sha256` lines) stay non-mechanical in this change.
- Nothing else about the classifier changes.

## Problem

`bborbe/homebrew-tap` receives one PR per released cask, authored by the release workflow as `github-actions[bot]`. 27 of the tap's 30 pull requests are single-file `+2 −2` bumps of a `Casks/<name>.rb`: the `version` line and its `sha256`. They are the largest block of live review work in the queue, and the classifier rejects all of them because cask files are not on its allow-list — so a two-line version bump waits behind the same LLM review as a feature PR.

## Goal

A cask bump that changes only one `version` line and one `sha256` line classifies as trivial, so the `autoMerge.trivial` opt-in and the mechanical override can act on it, while any cask change that touches anything else — or more hashes than one — still needs a review.

## Non-goals

- No change to the `go.mod`, `go.sum`, `Dockerfile`, `CHANGELOG.md` or workflow patterns.
- No change to `ClassifyTrivial`'s signature or its callers.
- No support for multi-architecture casks (four `sha256` lines in `on_macos` / `on_linux` blocks) — a follow-up, once one of them actually produces a pull request.
- No support for homebrew *formulae* (`.rb` files under `Formula/`) — none exist in the fleet today.
- No change to the `MECHANICAL_OVERRIDE_AUTHORS` gate or to the watcher's poll path.

## Assumptions

- The cask updater always bumps `version` and `sha256` together, because the sha256 is the hash of the artefact named by the version. A change to one without the other means a hand edit, not a release.
- Three of the tap's five casks (`dark-factory`, `distill`, `vault-cli`) are multi-architecture block form with four `sha256` lines; the other two (`vault-ui`, `git-ai-sync`) carry one. Only the latter two have an updater workflow, so only they produce pull requests today. Verified 2026-10-24 against the tap.
- GitHub returns the cask patch as a text unified diff, so change lines carry the `+` / `-` prefix the existing helpers match with, and real lines are indented two spaces inside the `cask` block.
- `github-actions[bot]` is on `TRUSTED_AUTHORS` in the stage, or the operator adds it — the mechanical gate and the arming path both require a trusted author.

## Acceptance Criteria

Every entry name in ACs 1-11 below contains `Casks`, so `go test ./pkg/ -v -ginkgo.v -ginkgo.focus='Casks'` selects exactly this set and nothing else. Each AC is satisfied when that focused run lists the named entries and reports 0 failures.

- [ ] A cask whose removed `version` and `sha256` lines are each paired with an added line of the same kind classifies as trivial. Evidence: the entry `Casks version+sha256 bump -> true`.
- [ ] A cask whose `version` line changes with no `sha256` change classifies as non-trivial. Evidence: the entry `Casks version only -> false`.
- [ ] A cask whose `sha256` line changes with no `version` change classifies as non-trivial. Evidence: the entry `Casks sha256 only -> false`.
- [ ] A cask change that also touches a `url` line classifies as non-trivial. Evidence: the entry `Casks url change -> false`.
- [ ] A removed `version` line paid for by an added `sha256` line classifies as non-trivial — pairing is by kind, never by count. Evidence: the entry `Casks cross-kind pairing -> false`.
- [ ] A removed `sha256` line with no added `sha256` line classifies as non-trivial. Evidence: the entry `Casks unpaired removal -> false`.
- [ ] An empty patch on a cask classifies as non-trivial. Evidence: the entry `Casks empty patch -> false`.
- [ ] A multi-architecture block-form bump — one `version` line and four paired `sha256` lines — classifies as non-trivial. Evidence: the entry `Casks multi-arch block bump -> false`.
- [ ] A non-Ruby file under `Casks/` (`Casks/README.md`) classifies as non-trivial, so the allow-list needs the `.rb` suffix and not just the directory prefix. Evidence: the entry `Casks non-ruby file -> false`.
- [ ] The real bump `bborbe/homebrew-tap#29` (`vault-ui 0.98.0`, one file, `+2 −2`) classifies as trivial. Its patch text is embedded verbatim in the prompt, because the container has no network. Evidence: the entry `Casks real homebrew-tap bump -> true`, with the fixture's origin in a comment above it.
- [ ] **Negative:** the real cask-addition PR `bborbe/homebrew-tap#3` — three files (`Casks/git-ai-sync.rb`, `.github/workflows/update-git-ai-sync-cask.yml`, `README.md`) — classifies as non-trivial, so the rule rejects the non-mechanical change it must not wave through. Evidence: the entry `Casks add-cask PR with workflow and readme -> false`.
- [ ] **Negative:** no pre-existing classifier entry was removed or weakened. Evidence: `git diff -U0 "$(git merge-base HEAD origin/master)" HEAD -- pkg/trivial_test.go | grep -cE '^-[^-]|^-$'` prints `0` (grep exits 1 at zero matches, which is the expected outcome here), and `go test ./pkg/ -v -ginkgo.focus='ClassifyTrivial'` reports 0 failures across every entry that existed before this change.
- [ ] **Post-Deploy (Rung-2):** the first cask bump PR on `bborbe/homebrew-tap` after the deploy carries the auto-merge label, an `APPROVED` review from `ben-s-pull-request-reviewer[bot]`, and reaches `MERGED`. Evidence: `gh -R bborbe/homebrew-tap pr view <n> --json labels,reviews,state` reports the label, the approve and `MERGED`.
  - `deploy_check:` `kubectlnukedev -n dev get deploy/github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MECHANICAL_OVERRIDE_AUTHORS")].value}' | grep -q 'github-actions' && kubectlnukedev -n dev get deploy/github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="TRUSTED_AUTHORS")].value}' | grep -q 'github-actions' && kubectlnukedev -n dev get deploy/github-pr-watcher -o jsonpath='{.spec.template.spec.containers[0].image}' | awk -F: '{print $NF}'`
  - `deploy_target:` `$(git describe --tags --abbrev=0)`, evaluated in the host repo checked out at the released master commit the deploy consumed

## Verification

### Container-executable (runs inside the YOLO container at prompt time)

- `make precommit` — fmt, generate, test, lint, vet, vuln, license
- `go test ./pkg/ -v -ginkgo.v -ginkgo.focus='Casks'` — the eleven new entries pass, `Ran 11 of`
- `grep -n 'Casks/' pkg/trivial.go` — the classifier covers the file, in code and in the allow-list doc comment

### Operator-executable (runs on the host after PR merge)

1. Confirm the tap's opt-in: `gh api repos/bborbe/homebrew-tap/contents/.maintainer.yaml --jq .content | base64 -d` must show `autoMerge.trivial: true` and `prReviewer.autoApprove: true`. The repo has **no** `.maintainer.yaml` today, so without this step the gate cannot fire there whatever the classifier says. Enabling it means `github-actions[bot]` PRs merge without a human on that repo — the operator's decision.
2. Add `github-actions[bot]` to **both** `TRUSTED_AUTHORS` and `MECHANICAL_OVERRIDE_AUTHORS` in `github-pr-reviewer/values-{dev,prod}.yaml` of the `nuke-dev` / `nuke-prod` repos. Trust is required twice over: the mechanical gate checks it, and `tryAutoMerge` refuses to arm an untrusted author.
3. Deploy the watcher release carrying this change, per stage.

## Desired Behavior

1. A file matching `Casks/<name>.rb` joins the mechanical file allow-list; any other file under `Casks/` stays non-mechanical.
2. Every changed line must be a `version "…"` or `sha256 "<64 hex>"` line, allowing the two-space indent real cask lines carry; a line that is not one makes the file non-mechanical.
3. Exactly one `version` line and exactly one `sha256` line may change, each removed line paired with an added line of the same kind. A version change without a sha256 change (or the reverse), a cross-kind pairing, or more than one `sha256` change line makes the file non-mechanical.
4. A real single-hash cask bump (one paired `version` change and one paired `sha256` change, nothing else) classifies as trivial end to end.

## Constraints

- **Conservative direction.** A false positive ships an unreviewed behavior change; a false negative costs one review. Every ambiguous cask shape resolves to non-mechanical.
- **Exactly one pair per kind.** A multi-architecture bump is out of scope, not a variant of the accepted shape; the count rule is what keeps this change small.
- **Paired by kind, not by count.** A `version` addition cannot pay for a `sha256` removal.
- **The allow-list doc comment is part of the contract.** `ClassifyTrivial`'s doc comment enumerates the allow-list; it gains the cask case in the same change, so the comment never describes less than the code.
- **Fixtures are embedded verbatim.** The container has no `gh` and no network; the real patches are pasted into the prompt, not fetched.
- **`github-actions[bot]` is a shared identity.** It authors every Actions workflow in the fleet, so a mechanical-approve entry for it is wider than a dedicated updater login. Accepted: the classifier accepts two line shapes under `Casks/` only, and the gate additionally requires the repo to opt in through both `.maintainer.yaml` flags. A dedicated cask-updater identity is the better long-term shape and a follow-up.
- **Pure function.** `ClassifyTrivial` stays a pure function over `[]PRFile` — no I/O, no context, no config.
- **Patch-only.** A missing patch is never mechanical.
- **Tests:** Ginkgo v2 / Gomega `DescribeTable` with one named `Entry` per shape, in the existing `pkg/trivial_test.go` style.

## Failure Modes

| Trigger | Expected behavior | Recovery |
|---|---|---|
| A multi-architecture cask gets bumped (four `sha256` lines) | Non-mechanical, so the PR gets a review | Intended for this change; widen in a follow-up once such a PR exists |
| A cask gains a new field (a `livecheck`, a `depends_on`) alongside a version bump | Non-mechanical | Intended: a new field is content |
| The updater writes the version and the hash in separate commits | The PR carrying both is mechanical; the single-line one is not | Intended: the pairing rule is the invariant |
| A `Casks/` file is edited by hand to a different URL | Non-mechanical | Intended |
| A non-Ruby file under `Casks/` changes | Non-mechanical; the allow-list requires the `.rb` suffix | Intended |
| The tap never opts in through `.maintainer.yaml` | Nothing changes: the classifier says trivial, the gate still refuses | Operator step 1 above; the review path still works |

## Security / Abuse

- The cask updater authors as `github-actions[bot]`, a generic workflow identity shared by every Actions workflow in the fleet — broader than a dedicated updater login. Three controls bound it: the classifier accepts only two line shapes under `Casks/*.rb`, the mechanical gate requires a trusted author, and the repo must opt in through both `.maintainer.yaml` flags. A workflow-authored PR that touches anything else still gets a review.
- Enabling `autoMerge.trivial` + `prReviewer.autoApprove` on a public repo is what lets `github-actions[bot]` PRs merge without a human there. That is an operator decision, recorded as a step, not a side effect of this change.
- The rule reads a fixed path suffix and two line shapes; it reads no credentials and writes nothing.

## Suggested Decomposition

| # | Prompt focus | Covers DBs | Covers ACs | Depends on |
|---|---|---|---|---|
| 1 | `Casks/<name>.rb` in `isMechanicalFile` with the one-version-one-sha256 pairing rule, the doc-comment update, and the eleven entries | 1, 2, 3, 4 | 1-12 | — |

Rationale: one layer (`pkg/trivial.go` plus its test), one prompt. The Post-Deploy AC (13) is verified after the dev deploy, not by a prompt.

## Do-Nothing Option

Every cask bump keeps costing one LLM review and one queue slot — 27 of the tap's 30 pull requests, for a two-line change that is mechanically verifiable.
