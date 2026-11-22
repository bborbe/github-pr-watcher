---
status: approved
spec: [006-park-age-metric]
created: "2026-11-22T15:46:00Z"
queued: "2026-11-22T14:57:22Z"
branch: dark-factory/park-age-metric
---

<summary>
- Every currently-parked oversized PR is now published on the watcher's `/metrics` as `github_pr_watcher_parked_prs{repo,pr}` in seconds.
- The scan collects the parked set exactly where it already drops those PRs, so the park decision itself is untouched.
- The published set is replaced per complete scan: a park that ends (closed, drafted, or pushed under the limit) disappears within one scan, and a failed or skipped scan leaves the previous values in place.
- A park's age comes from the ledger, not the clock: a comment does not reset it, a new head SHA restarts it, and a watcher restart continues it.
- The ledger is read at scan start and rewritten at scan end; its path is configurable through a new `PARK_LEDGER_PATH` env var (default `/data/park-ledger.json`).
- A ledgered PR is re-evaluated even while inside the unreviewed threshold, so a push that takes it under the size limit clears its series promptly instead of lingering.
- The new metric and the interface method are registered and mocked, and the existing unreviewed gauge and park behaviour are unchanged.
- Rung-3 post-deploy checks (live series, restart survival, ledger contents on the pod) remain operator-side after merge.
</summary>

<objective>
Wire the `ParkLedger` from the previous prompt into `UnreviewedScanner.Scan`, add a `SetParkedPRs` method and the `github_pr_watcher_parked_prs` gauge to `Metrics`, and thread a new `PARK_LEDGER_PATH` config value from `main.go` through `factory.CreateWatcher` into the scanner. A complete scan publishes the age of every parked oversized PR; an aborted scan publishes nothing and leaves the previous values.
</objective>

<context>
Read `docs/dod.md`. There is no `CLAUDE.md` in this repo.

Read these files in full before changing anything:

- `pkg/park_ledger.go` (from the previous prompt) — `ParkLedger`, `ParkLedgerEntry`, `ParkObservation`, `ParkedPRStart`, `NewParkLedger`, `AgeSeconds`, `Read`, `Has`, `Reconcile`.
- `pkg/metrics.go` — the `Metrics` interface, `UnreviewedPR`, the `unreviewedPRs` `GaugeVec`, `prometheusMetrics.mu`, `SetUnreviewedPRs` (the Reset+Set pattern), and the `init()` `prometheus.MustRegister` list.
- `pkg/metrics_unreviewed_test.go` — the `GatherAndCompare` / `GatherAndCount` pattern and the rule "never probe absence with `WithLabelValues`, it re-creates the series".
- `pkg/metrics_export_test.go` — the test accessors; add a parked one only if you need to read the gauge directly (the registry test can use `GatherAndCompare`).
- `pkg/unreviewed_scanner.go` — the whole file, especially `NewUnreviewedScanner`, the `unreviewedScanner` struct, `Scan` (lock, `now := s.currentDateTime.Now()`, the PR loop, the abort `return`s, `SetUnreviewedPRs`, `pruneState`), `qualify`, `parked`, `cachedReviewed`, and `pruneState`.
- `pkg/watcher.go` — `prKey(pr PullRequest) string` (`fmt.Sprintf("%s/%s#%d", ...)`, i.e. `owner/repo#n`), and `oversized` / `Exclusion` / `ReviewIgnoreExclusion`.
- `pkg/factory/factory.go` — `CreateWatcher` (the `dispatchLedger pkg.DispatchLedger` parameter and the `pkg.NewUnreviewedScanner(...)` call at ~line 140).
- `main.go` — the args struct with `env:` tags (e.g. `UnreviewedRetryMax`), the `pkg.ParseUnreviewedConfig` call, and the `factory.CreateWatcher(...)` call at ~line 396.
- `pkg/unreviewed_scanner_test.go` — the `BeforeEach`, `openPR`, `searchOpenReturns`, `defaultFilter`, `trustAlice`, `newScanner`, `qualifying`, `headDetails`, the `os.MkdirTemp` temp-file pattern, and the `fakeMetrics.SetUnreviewedPRsArgsForCall` / `CallCount` assertions.

Coding-guide references (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-functional-options-pattern.md`
</context>

<requirements>
1. **Metric — `pkg/metrics.go`.** Add:
   - To the `Metrics` interface, next to `SetUnreviewedPRs`:
     ```go
     // SetParkedPRs replaces the set of oversized PRs reported on the
     // github_pr_watcher_parked_prs gauge. The slice is the complete result of
     // one scan, so an empty slice clears the gauge.
     SetParkedPRs(prs []ParkedPR)
     ```
   - An exported input type:
     ```go
     // ParkedPR identifies one oversized PR parked at human_review, with how
     // long it has been parked.
     type ParkedPR struct {
         Repo       string
         Number     int
         AgeSeconds float64
     }
     ```
   - A package-level gauge vec alongside `unreviewedPRs`:
     ```go
     parkedPRs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
         Name: "github_pr_watcher_parked_prs",
         Help: "Seconds each open, trusted-author, oversized pull request has been continuously parked at human_review, by repo and PR number. Seeded from the PR's updated-at on the first scan that observes it parked and held in a ledger until its head SHA changes; a complete scan replaces the whole set, so a park that ends disappears within one scan. Untrusted-author parks are not covered. Single replica only.",
     }, []string{"repo", "pr"})
     ```
   - Register `parkedPRs` in the `init()` `prometheus.MustRegister(...)` list.
   - Implement:
     ```go
     func (m *prometheusMetrics) SetParkedPRs(prs []ParkedPR) {
         m.mu.Lock()
         defer m.mu.Unlock()
         parkedPRs.Reset()
         for _, pr := range prs {
             parkedPRs.WithLabelValues(pr.Repo, strconv.Itoa(pr.Number)).Set(pr.AgeSeconds)
         }
     }
     ```
     Reuse the existing `m.mu` (it already serialises the Reset+Set pair). Do NOT change any existing metric name or help string.

2. **Regenerate the mock.** Run `make generate` (or `go generate ./...`) so `mocks/metrics.go` gains `SetParkedPRsStub`, `SetParkedPRsCallCount`, and `SetParkedPRsArgsForCall`. Do not hand-edit the generated file beyond what the generator produces.

3. **Scanner option — `pkg/unreviewed_scanner.go`.** Add an optional dependency so existing callers compile unchanged:
   ```go
   // UnreviewedScannerOption configures optional scanner dependencies.
   type UnreviewedScannerOption func(*unreviewedScanner)

   // WithParkLedger wires the park ledger the scan records parked oversized PRs
   // into and reads their start times from. Without it the parked-PR metric is
   // not published, which is the pre-change behaviour.
   func WithParkLedger(ledger ParkLedger) UnreviewedScannerOption {
       return func(s *unreviewedScanner) { s.parkLedger = ledger }
   }
   ```
   Add `parkLedger ParkLedger` to the `unreviewedScanner` struct, change the constructor to `NewUnreviewedScanner(..., cfg UnreviewedConfig, opts ...UnreviewedScannerOption) UnreviewedScanner`, build the struct into a local variable, apply `for _, opt := range opts { opt(scanner) }`, and return it. Existing positional callers in `pkg/unreviewed_scanner_test.go`, `pkg/watcher_test.go`, and `pkg/factory/factory.go` must keep compiling; only the factory adds the option.

4. **Collect the parked set in `Scan`.** Change `qualify`'s signature to return the parked answer as a fourth value:
   ```go
   func (s *unreviewedScanner) qualify(
       ctx context.Context,
       pr PullRequest,
       now libtime.DateTime,
       ledgered bool,
   ) (PRDetails, bool, bool, error)
   ```
   The return order is `(details, counted, parked, err)`. Apply these changes to `qualify`:
   - The task-creation-filter skip and the override-label skip return `PRDetails{}, false, false, nil` (a draft/override is not parked, regardless of `ledgered`).
   - Guard the threshold gate and the reviewed cache with `!ledgered`:
     ```go
     if !ledgered && now.Sub(pr.UpdatedAt) < s.cfg.Threshold {
         return PRDetails{}, false, false, nil
     }
     if !ledgered && s.cachedReviewed(pr) {
         return PRDetails{}, false, false, nil
     }
     ```
     A ledgered PR is re-evaluated even inside the threshold, because the ledger entry is authoritative until its head SHA changes and a push under the limit must clear the series within one scan.
   - `GetPRDetails` error → `return PRDetails{}, false, false, err`; trust-check error → `return PRDetails{}, false, false, errors.Wrapf(...)`.
   - `!trustResult.Success()` → `return PRDetails{}, false, false, nil` (untrusted-author parks are not covered).
   - empty `details.HeadSHA` → `return PRDetails{}, false, false, nil`.
   - Replace the parked drop with:
     ```go
     if s.parked(ctx, pr, details) {
         return details, false, true, nil
     }
     ```
   - After the parked check, when NOT parked and `ledgered`, preserve the unreviewed gate for counting before continuing to the reviews:
     ```go
     if ledgered {
         if now.Sub(pr.UpdatedAt) < s.cfg.Threshold {
             return PRDetails{}, false, false, nil
         }
         if s.cachedReviewed(pr) {
             return PRDetails{}, false, false, nil
         }
     }
     ```
   - `ListReviews` error → `return PRDetails{}, false, false, err`; a matching reviewer verdict (after `CompleteRedriven`) → `return PRDetails{}, false, false, nil`; final → `return details, true, false, nil`.

5. **`Scan` changes.**
   - After `defer s.mu.Unlock()` and before `fetchAllOpenPRs`, read the ledger if present:
     ```go
     if s.parkLedger != nil {
         s.parkLedger.Read(ctx)
     }
     ```
   - Before the PR loop, initialise `observations := make([]ParkObservation, 0)`.
   - In the loop, compute `ledgered := s.parkLedger != nil && s.parkLedger.Has(prKey(pr))` and call `details, counted, parked, err := s.qualify(ctx, pr, now, ledgered)`.
   - On `err != nil`, keep the existing abort (`glog.Warningf` + `return`) — the abort paths must NOT reconcile or publish.
   - When `parked`, append `ParkObservation{Repo: pr.Owner + "/" + pr.Repo, Number: pr.Number, SHA: details.HeadSHA, UpdatedAt: pr.UpdatedAt}` and `continue`.
   - When `!counted`, `continue`; otherwise append the candidate as today.
   - After the complete scan's existing `SetUnreviewedPRs(...)` and `s.pruneState(prs)` calls, publish the parked set:
     ```go
     if s.parkLedger != nil {
         starts := s.parkLedger.Reconcile(ctx, observations)
         parkedPRs := make([]ParkedPR, 0, len(starts))
         for _, st := range starts {
             parkedPRs = append(parkedPRs, ParkedPR{
                 Repo:       st.Repo,
                 Number:     st.Number,
                 AgeSeconds: AgeSeconds(now, st.Start),
             })
         }
         s.metrics.SetParkedPRs(parkedPRs)
     }
     ```
     A `Reconcile` that fails to write still returns the reconciled starts, so the gauge is published from the in-memory result. A complete scan with no parked PRs passes an empty slice, which clears the gauge.

6. **Factory wiring — `pkg/factory/factory.go`.** Add `parkLedger pkg.ParkLedger` to `CreateWatcher`'s parameter list immediately after `dispatchLedger pkg.DispatchLedger`, and pass `pkg.WithParkLedger(parkLedger)` to `pkg.NewUnreviewedScanner(...)`.

7. **Config + main — `main.go`.**
   - Add to the args struct (adjacent to `UnreviewedRetryMax`):
     ```go
     ParkLedgerPath string `required:"false" arg:"park-ledger-path" env:"PARK_LEDGER_PATH" usage:"Path to the JSON ledger the watcher writes parked-PR start times to (mounted on a PersistentVolumeClaim at /data)" default:"/data/park-ledger.json"`
     ```
   - Before the `factory.CreateWatcher(...)` call, construct `parkLedger := pkg.NewParkLedger(a.ParkLedgerPath)`.
   - Pass `parkLedger` into `factory.CreateWatcher(...)` in the position matching the new parameter (immediately after `dispatchLedger`).

8. **Tests — new file `pkg/metrics_parked_test.go`, package `pkg_test`, Ginkgo/Gomega.**
   - `SetParkedPRs` with one entry, then assert the series and value with `testutil.GatherAndCompare(prometheus.DefaultGatherer, strings.NewReader(expected), "github_pr_watcher_parked_prs")`. The expected text must match the help string verbatim:
     ```
     # HELP github_pr_watcher_parked_prs Seconds each open, trusted-author, oversized pull request has been continuously parked at human_review, by repo and PR number. Seeded from the PR's updated-at on the first scan that observes it parked and held in a ledger until its head SHA changes; a complete scan replaces the whole set, so a park that ends disappears within one scan. Untrusted-author parks are not covered. Single replica only.
     # TYPE github_pr_watcher_parked_prs gauge
     github_pr_watcher_parked_prs{pr="42",repo="bborbe/repo"} 3600
     ```
   - Full-replacement: two series → `GatherAndCount` 2; one → 1; `SetParkedPRs(nil)` → 0. End the spec with `SetParkedPRs(nil)` so the shared registry is clean for later specs.

Both new test files (`pkg/metrics_parked_test.go` and `pkg/unreviewed_scanner_parked_test.go`) carry the standard `// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / BSD header, matching every other file in `pkg/`.

9. **Tests — new file `pkg/unreviewed_scanner_parked_test.go`, package `pkg_test`, Ginkgo/Gomega.** Build the scanner with `maxAdditions` small enough that the PR is oversized (e.g. 10) and a temp ledger path (`os.MkdirTemp` + `filepath.Join(tmpDir, "park-ledger.json")`), passed via `pkg.WithParkLedger(pkg.NewParkLedger(path))`. Use `GetPRDetailsReturns(pkg.PRDetails{HeadSHA: ..., Additions: 20, ChangedFiles: 1, ...}, nil)`; the default `GetReviewIgnore` fake returning `(nil, nil)` makes the exclusion empty. Assert through `fakeMetrics.SetParkedPRsArgsForCall(...)` / `SetParkedPRsCallCount()`:
   - **Publishes a parked PR:** one open PR `updatedAt = now-1h`, oversized, trusted, not drafted; after `Scan`, `SetParkedPRs` is called once with one `pkg.ParkedPR` for `bborbe/repo` #42 and `AgeSeconds ~ 3600 ± 60`; the ledger file contains `bborbe/repo#42` with the head SHA.
   - **A comment does not reset:** first scan parks it (ledger start = `now-1h`); change `SearchOpenPRs`/`GetPRDetails` to the SAME head SHA but `updatedAt = now`; second scan (same ledger path) still publishes `AgeSeconds ~ 3600 ± 60`.
   - **A push under the limit clears it within one scan:** first scan parks it (oversized, sha `"sha-a"`); second scan returns sha `"sha-b"` with `Additions: 2` (under the limit) and `updatedAt = now` (a push moves it); after the second scan `SetParkedPRs` is called with an empty slice and the ledger key is gone.
   - **Closed PR clears it:** first scan parks it; second scan returns an empty open-PR search; `SetParkedPRs` is called with an empty slice.
   - **Aborted scan leaves the previous values:** first scan parks it; second scan makes `GetPRDetailsReturns(..., errors.Errorf(ctx, "boom"))`; `SetParkedPRsCallCount()` does not increase on the second scan.
<!--
OPEN QUESTION (reviewer): the spec's Assumptions list open / new push / draft->ready as the things
that move updatedAt, while Desired Behavior 3 and AC case 2 say a comment moves it too. To satisfy
AC case 2 at the scanner level (not only inside the ledger), a PR already in the ledger is
re-evaluated for parking inside the unreviewed threshold (the `ledgered` bypass in requirement 4);
its unreviewed counting is preserved by the post-parked threshold/cache guards. Under the naive
"prune = not observed this scan" rule, AC case 2 and AC 4 (push-under-limit within one scan) cannot
both hold.
-->
</requirements>

<constraints>
- Go, following the repo's existing patterns: Ginkgo/Gomega tests in `pkg_test`, `github.com/bborbe/errors` for wrapping, `libtime` where the existing code uses it, Counterfeiter mocks under `mocks/`.
- Frozen interfaces the nuke alert rule and verification commands depend on: metric name `github_pr_watcher_parked_prs`, labels `repo` and `pr`, value in seconds; env var `PARK_LEDGER_PATH`, default `/data/park-ledger.json`; ledger file shape `{"entries":{"<owner>/<repo>#<pr>":{"start":"<RFC3339>","sha":"<head sha>"}}}`.
- The park decision itself is unchanged: do not touch `buildHumanReviewFrontmatter`, `buildParkedBody`, `func oversized`, `MaxChangedFiles`, or `MaxAdditions`, and do not alter what the poll path publishes for a parked PR.
- Do NOT change the unreviewed gauge's set or labels. The only behavioural change for the unreviewed scan is that a ledgered PR is re-evaluated for parking inside the threshold; its unreviewed counting is preserved by the post-parked threshold/cache guards in requirement 4.
- A failed or skipped scan publishes nothing and leaves the previous gauge values in place; the abort `return`s in `Scan` must stay before the parked reconcile/publish.
- Do NOT touch `bborbe/nuke` or any file outside this repo. This project holds credentials for `bborbe/github-pr-watcher` only.
- No new external dependency.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass. Rung-3 post-deploy checks are operator-side and are NOT verified by this prompt.
</constraints>

<verification>
- `go test ./pkg/...` — the Ginkgo suite (do NOT use `-run`; the suite entry point is `TestSuite`, and `-run` matches nothing and exits 0 falsely). Must pass, including the new metric and scanner tests and all pre-existing park/unreviewed tests unchanged.
- `make precommit` — format, generate, test, lint, security checks. Must pass.
- `grep -n 'github_pr_watcher_parked_prs' pkg/metrics.go` — the metric name is registered.
- `grep -n 'SetParkedPRs' pkg/metrics.go mocks/metrics.go` — the interface method and its regenerated mock are present.
- `grep -n 'PARK_LEDGER_PATH' main.go` — the env var is wired.
- `grep -n 'WithParkLedger' pkg/unreviewed_scanner.go pkg/factory/factory.go` — the scanner option is wired through the factory.
</verification>
