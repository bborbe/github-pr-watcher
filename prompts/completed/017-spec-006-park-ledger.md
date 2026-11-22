---
status: completed
spec: [006-park-age-metric]
summary: Added pkg/park_ledger.go with a durable ParkLedger that seeds/freezes/restarts/prunes park start times by head SHA, persists atomically under a 2s timeout, plus AgeSeconds and the eight requested Ginkgo tests; updated CHANGELOG.
execution_id: 3546659f-32d3-562a-a7c7-870cb565441e-exec-017-spec-006-park-ledger
dark-factory-version: dev
created: "2026-11-22T15:45:00Z"
queued: "2026-11-22T14:51:07Z"
started: "2026-11-22T14:51:09Z"
completed: "2026-11-22T15:01:53Z"
branch: dark-factory/park-age-metric
---

<summary>
- Adds a small, self-contained ledger that remembers when each oversized PR parked and which head SHA it parked at.
- The ledger file is JSON on disk, read at the start of a scan and rewritten atomically at its end, so a watcher restart does not reset a park's age.
- A park's start is seeded from the PR's own updated-at the first time it is observed parked, then frozen until the head SHA changes.
- A comment that moves updated-at does not reset the age; a push to a new head SHA does.
- Entries for PRs that are no longer observed as parked are dropped each scan, so the file only tracks the live parked set.
- A missing file, a corrupt file, a torn write, or a failed write never stops the scan; the in-memory ledger stays authoritative.
- Ships the four ledger-decides-the-age unit tests the spec's acceptance criterion names.
- This prompt is the ledger module only. Wiring it into the scan and publishing the metric is the next prompt.
</summary>

<objective>
Create `pkg/park_ledger.go` with a `ParkLedger` that persists park start times as `{"entries":{"<owner>/<repo>#<pr>":{"start":"<RFC3339>","sha":"<head sha>"}}}` under an operator-supplied path, reconciles each scan's parked observations into that file (seed from updated-at, freeze on unchanged SHA, restart on a changed SHA, prune the absent), and returns the reconciled starts plus an age helper. This is the durable state the parked-PR gauge in the next prompt reads.
</objective>

<context>
Read `docs/dod.md`. There is no `CLAUDE.md` in this repo.

Read these files in full before changing anything — they are the patterns this module must match:

- `pkg/cursor.go` — `LoadCursor` / `SaveCursor`, the atomic write shape (`os.WriteFile(path+".tmp", data, 0600)` then `os.Rename`), the `#nosec` comments, and how a missing/corrupt file is tolerated. The park ledger's read/write is the same idea with a richer document.
- `pkg/dispatch_ledger.go` and `pkg/override_ledger.go` — the in-process ledger interface style (`interface`, `NewXxx`, unexported struct, `sync.Mutex`).
- `pkg/metrics.go` — `libtime` usage and the `//counterfeiter:generate` convention (you are NOT changing metrics in this prompt; just match the package conventions).
- `pkg/pkg_suite_test.go` — the Ginkgo suite entry point is `TestSuite`, so `go test ./pkg/...` runs the suite. Do NOT use `-run` to filter (it matches nothing and exits 0 falsely).
- `pkg/cursor_test.go` — the `os.MkdirTemp` + `filepath.Join` temp-file pattern and the missing/corrupt-file assertions you should mirror.

Verify the `libtime` API you rely on (already checked for you, but confirm before use):
- `libtime.DateTime` marshals to JSON as an RFC3339Nano string and unmarshals via `ParseTime`; its zero value marshals to `null`.
- `now.Sub(start)` where both are `libtime.DateTime` returns `libtime.Duration`; `.Duration()` returns the stdlib `time.Duration`, which has `.Seconds()`.

Coding-guide references (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-glog-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-concurrency-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-context-cancellation-in-loops.md`
</context>

<requirements>
1. **New file `pkg/park_ledger.go`.** Define, in package `pkg`:

   ```go
   // ParkLedgerEntry is one persisted park: when it started and the head SHA it
   // started at. The SHA is what makes a comment harmless and a push a restart.
   type ParkLedgerEntry struct {
       Start libtime.DateTime `json:"start"`
       SHA   string           `json:"sha"`
   }

   // ParkObservation is one PR this complete scan evaluated and found parked.
   // UpdatedAt is the PR's search-result updated-at, the clock the scan already
   // treats as reviewable-since.
   type ParkObservation struct {
       Repo      string // "owner/repo"
       Number    int
       SHA       string
       UpdatedAt libtime.DateTime
   }

   // ParkedPRStart is a reconciled ledger entry with its identity restored, so
   // the caller can publish an age without parsing the ledger key.
   type ParkedPRStart struct {
       Repo   string
       Number int
       Start  libtime.DateTime
   }

   // ParkLedger persists the start of each currently-parked oversized PR.
   type ParkLedger interface {
       // Read loads the file into memory. A missing file leaves the ledger
       // empty and logs at INFO; a corrupt file logs an error naming the path
       // and keeps the entries already in memory. It never returns an error:
       // ledger I/O must never fail a scan.
       Read(ctx context.Context)
       // Has reports whether key ("<owner>/<repo>#<pr>") is currently in the
       // in-memory ledger.
       Has(key string) bool
       // Reconcile seeds a new entry from the observation's UpdatedAt, keeps an
       // unchanged entry's Start, restarts a changed-SHA entry at UpdatedAt,
       // drops every entry absent from observations, persists the result
       // atomically, and returns the reconciled starts. A write failure is
       // logged and the in-memory result stays authoritative.
       Reconcile(ctx context.Context, observations []ParkObservation) []ParkedPRStart
   }

   // NewParkLedger returns a ParkLedger backed by the file at path.
   func NewParkLedger(path string) ParkLedger

   // AgeSeconds reports how many seconds have elapsed from start to now,
   // clamped at 0 so a host clock skew never publishes a negative age.
   func AgeSeconds(now, start libtime.DateTime) float64
   ```

   The unexported implementation is `parkLedger` with fields `path string`, `mu sync.Mutex`, `entries map[string]ParkLedgerEntry`. Use the key form `fmt.Sprintf("%s#%d", obs.Repo, obs.Number)`; this matches `prKey(pr)` in `pkg/watcher.go` (`owner/repo#n`), which the scan will pass to `Has`.

   The persisted document is:
   ```go
   type parkLedgerFile struct {
       Entries map[string]ParkLedgerEntry `json:"entries"`
   }
   ```
   so the on-disk shape is exactly `{"entries":{"<owner>/<repo>#<pr>":{"start":"<RFC3339>","sha":"<head sha>"}}}`.

2. **`Read(ctx)` behavior** — wrap the read in a 2 s timeout so a slow filesystem cannot delay the scan. Read `path`:
   - missing file (`os.IsNotExist`): set `entries` to a fresh empty map and log at INFO (`glog.Infof("park ledger not found, starting empty path=%s", path)`).
   - corrupt/unparseable JSON: log an error naming the path (`glog.Errorf("park ledger corrupt path=%s err=%v", path, err)`) and KEEP the existing in-memory `entries` unchanged.
   - any other read error (permission, timeout): log an error naming the path and KEEP the existing in-memory `entries`.
   - success: replace `entries` with the decoded map; if `Entries` is nil, set it to an empty map.

   A torn write leaves `path+".tmp"` behind; `Read` reads only `path`, so the previous ledger stands. Do not attempt to read or clean the temp file.

3. **`Reconcile(ctx, observations)` behavior.** Under `mu`, build a fresh `next map[string]ParkLedgerEntry` and a `starts []ParkedPRStart`:
   - For each observation, `key := fmt.Sprintf("%s#%d", obs.Repo, obs.Number)`.
   - `start := obs.UpdatedAt`. If `l.entries[key]` exists AND its `SHA == obs.SHA`, set `start = existing.Start` (freeze on unchanged SHA — a comment cannot reset it).
   - Store `next[key] = ParkLedgerEntry{Start: start, SHA: obs.SHA}` and append `ParkedPRStart{Repo: obs.Repo, Number: obs.Number, Start: start}`.
   - Replace `l.entries = next` (this is the prune: keys absent from `observations` are gone).
   - Persist `next` atomically: marshal `parkLedgerFile{Entries: next}`, write `path+".tmp"` with mode `0600` under a 2 s timeout, then `os.Rename(path+".tmp", path)`. On a marshal/write/rename error, log an error naming the path and return `starts` anyway — the in-memory ledger stays authoritative and the next scan rewrites the file.
   - Return `starts` (in observation order; duplicate observations are not expected).

4. **2 s timeout without blocking.** `os.ReadFile`/`os.WriteFile` cannot be cancelled, so implement the timeout with a helper that runs the operation in a goroutine and `select`s on a buffered result channel versus `context.WithTimeout(ctx, 2*time.Second).Done()`. On timeout, log and return a wrapped error; the buffered channel prevents the goroutine from blocking forever. Keep the helper unexported and reuse it for read and write.

5. **`AgeSeconds(now, start)`** returns `now.Sub(start).Duration().Seconds()`, returning `0` when the value is negative.

6. **Tests — new file `pkg/park_ledger_test.go`, package `pkg_test`, Ginkgo/Gomega.** Use `os.MkdirTemp("", "park-ledger-*")` and `filepath.Join(tmpDir, "park-ledger.json")`, with `defer os.RemoveAll(tmpDir)`. Cover:
   - **AC 5 case 1 — same SHA, updatedAt now, start 3600 s ago → 3600.** Pre-write the ledger with `{key: {start: now-1h, sha: "sha-a"}}`; `Reconcile` an observation `{Repo:"bborbe/repo", Number:42, SHA:"sha-a", UpdatedAt: now}`; assert `AgeSeconds(now, starts[0].Start)` is `BeNumerically("~", 3600, 60)`. Proves the ledger, not updated-at, is the source.
   - **AC 5 case 2 — same SHA, updatedAt moved forward as a comment would, start 3600 s ago → 3600.** Pre-write start `now-1h`, sha `"sha-a"`; reconcile `{SHA:"sha-a", UpdatedAt: now}`; assert `~3600 ± 60`. Proves a comment does not reset a park.
   - **AC 5 case 3 — different SHA, updatedAt now, start 3600 s ago → 0.** Pre-write start `now-1h`, sha `"sha-a"`; reconcile `{SHA:"sha-b", UpdatedAt: now}`; assert `BeNumerically("~", 0, 60)`. Proves a push restarts the park.
   - **AC 5 case 4 — no ledger entry, updatedAt 3600 s ago → 3600.** Use a path that does not exist; `Read`; reconcile `{SHA:"sha-a", UpdatedAt: now-1h}`; assert `~3600 ± 60`. Proves a new entry is seeded from updated-at, not the observation time.
   - **Prune:** seed two entries, reconcile one observation; unmarshal the file and assert only the observed key remains.
   - **Round-trip / file shape:** after `Reconcile`, unmarshal the file into a local `struct{ Entries map[string]pkg.ParkLedgerEntry \`json:"entries"\` }` and assert the key carries both a non-zero `Start` and the observed `SHA`; assert `os.Stat(path).Mode().Perm() == 0o600`.
   - **Missing file:** `Read` on a non-existent path does not panic and a following `Reconcile` seeds and writes the file.
   - **Corrupt file keeps memory:** `Reconcile` to seed an in-memory entry (`UpdatedAt: now-1h`, sha `"sha-a"`); overwrite `path` with `{not json`; `Read`; then `Reconcile` the same key/sha with `UpdatedAt: now` and assert the returned `Start` is still `now-1h` (memory survived) and the file is valid JSON again.

   The four AC 5 tests assert the exact value the gauge will publish, via the shared `AgeSeconds` helper the next prompt also uses.

7. Add the standard `// Copyright (c) 2026 Benjamin Borbe All rights reserved.` / BSD header to both new files, matching every other file in `pkg/`.
<!--
OPEN QUESTION (reviewer): the spec's AC "The ledger, not the clock, decides the age" is headed
"three unit tests" but enumerates four cases. All four are implemented above. They assert the age via
the shared AgeSeconds helper — the exact value the gauge publishes in prompt 2 — because the ledger
module does not itself touch the metric.
-->
</requirements>

<constraints>
- Go, following the repo's existing patterns: Ginkgo/Gomega tests in `pkg_test`, `github.com/bborbe/errors` for wrapping, `libtime` where the existing code uses it.
- Frozen ledger file shape (the nuke alert rule and verification commands depend on it): `{"entries":{"<owner>/<repo>#<pr>":{"start":"<RFC3339>","sha":"<head sha>"}}}`. The path env var (`PARK_LEDGER_PATH`, default `/data/park-ledger.json`) is wired in the next prompt; this prompt only takes a `path string`.
- No new external dependency for storage. A file on the mounted path is sufficient.
- Writes are atomic: temp file in the same directory, then `rename`; the temp file is `0600`.
- The scan must not block on ledger I/O: both read and write carry a 2 s timeout and a slow/failed one must not fail the scan.
- Do NOT change `pkg/metrics.go`, `pkg/unreviewed_scanner.go`, `pkg/factory/`, or `main.go` in this prompt — those are the next prompt's scope. This prompt must leave the existing package compiling and its tests green.
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- The Rung-3 post-deploy acceptance criteria (live series, restart survival, ledger pruning on the pod) are operator-side after merge and are NOT verified by this prompt.
</constraints>

<verification>
- `go test ./pkg/...` — the Ginkgo suite (do NOT use `-run`; the suite entry point is `TestSuite`, and `-run` matches nothing and exits 0 falsely). Must pass, including the new `park_ledger_test.go`.
- `make precommit` — format, generate, test, lint, security checks. Must pass.
- `grep -n 'func NewParkLedger\|func AgeSeconds\|type ParkLedger interface' pkg/park_ledger.go` — the module is present.
</verification>
