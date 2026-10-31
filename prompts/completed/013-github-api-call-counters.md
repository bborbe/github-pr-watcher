---
status: completed
summary: Added per-caller github_api_calls_total and github_api_rate_limited_total counters recorded at rateCapturingTransport with request-path-derived buckets, wired through NewGitHubClient and CLUSTER config, with full tests, README and changelog updates
execution_id: github-pr-watcher-api-counters-exec-013-github-api-call-counters
dark-factory-version: v0.198.7
created: "2026-10-31T14:07:56Z"
queued: "2026-10-31T14:13:59Z"
started: "2026-10-31T14:16:23Z"
completed: "2026-10-31T14:21:30Z"
---

# Add per-caller GitHub API call and rate-limited counters at the transport

<summary>
- Every GitHub API request the watcher's GitHub client sends is now counted, including requests that fail before any response arrives.
- Each count says which rate-limit bucket the request spent (search, graphql or core), worked out from the request itself, so a response header naming another bucket can never misattribute a call.
- Each count says whether the call succeeded or failed, and rate-limit rejections (429, or a 403 that carries the quota-exhausted or retry-after signal) are counted separately.
- Rate-limit rejections carry a fixed "unclassified" class for now, because no rule exists yet to tell primary from secondary limiting.
- Every series carries the service name, the GitHub App id and the cluster, so several services sharing one App can be told apart; no token or secret ever appears in a label.
- The cluster is a new optional setting that defaults to "nuke".
- All expected series exist at zero from startup, so alert rules see a flat zero rather than missing data.
- The existing remaining-quota gauge and the non-2xx log line behave exactly as before.
</summary>

<objective>
Add two counters, `github_api_calls_total{service,credential,bucket,outcome,cluster}` and `github_api_rate_limited_total{service,credential,bucket,class,cluster}`, recorded at the single existing GitHub capture point `rateCapturingTransport.RoundTrip`, so operators can see which caller spends which GitHub rate-limit bucket and how often it is throttled. The bucket is derived from the request path only, which is what makes the attribution correct when responses from different buckets interleave.
</objective>

<context>
Read `CLAUDE.md` if present and `docs/dod.md`.

Read these coding-plugin docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-prometheus-metrics-guide.md` — especially `RULE go-prometheus/counter-pre-initialization` and `RULE go-prometheus/help-string-quality`.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-mocking-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-factory-pattern.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`

Read these source files fully before changing them:
- `pkg/githubclient.go` — `NewGitHubClient(httpClient *http.Client) GitHubClient` (wraps `httpClient.Transport` in a `rateCapturingTransport{inner, set, log}`), the `rateCapturingTransport` struct and its doc comment, `RoundTrip` (today: `resp, err := t.inner.RoundTrip(req)` then `if err != nil || resp == nil { return resp, err }` then `t.captureRemaining(resp)` then the `logNon2xxResponse` call), and `captureRemaining` (core-only gauge capture keyed on `X-RateLimit-Resource`).
- `pkg/metrics.go` — the package-level `var (...)` block of `prometheus.NewCounterVec(...)` collectors, the `init()` that calls `prometheus.MustRegister(...)` and pre-initialises label values with `.WithLabelValues(...).Add(0)`, and the `//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics` annotation style.
- `pkg/metrics_export_test.go` — package `pkg` accessors (`PRPublishedTotalForTest`, `UnreviewedRetriesTotalForTest`, …) that expose collectors to the external test package.
- `pkg/metrics_unreviewed_test.go` — external-package use of `testutil.ToFloat64` and `testutil.GatherAndCount(prometheus.DefaultGatherer, <name>)`.
- `pkg/rate_capture_test.go` and `pkg/non2xx_log_test.go` — internal (`package pkg`) tests that build `&rateCapturingTransport{inner: ..., set: ..., log: ...}` literals WITHOUT any metrics field. They must keep compiling and passing unchanged.
- `pkg/githubclient_test.go` — external `pkg_test` tests build clients via `pkg.NewForTest(...)` (defined in `pkg/githubclient_export_test.go`), which bypasses `NewGitHubClient`; unaffected.
- `pkg/pkg_suite_test.go` — the existing Ginkgo suite (`package pkg_test`) and its `//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6@v6.12.2 -generate` directive; `make precommit` runs `generate`, which rebuilds `mocks/`.
- `main.go` — the `application` struct (fields `AppID int64` with `env:"APP_ID"`, `Stage string` with `env:"STAGE"`; copy that tag layout), and `Run` where `ghClient := pkg.NewGitHubClient(httpClient)` is followed by `metrics := pkg.NewMetrics()`. `strconv` is already imported in `main.go`.
- `README.md` — the `## Environment Variables` table and the `## Metrics` table (its intro line says all metrics use the `github_pr_watcher_` prefix).

Verified facts:
- `pkg.NewGitHubClient` has exactly one production caller: `main.go`. No test calls it today.
- Prometheus is `github.com/prometheus/client_golang/prometheus` (v1.25.0, already a direct dependency); test helpers are `github.com/prometheus/client_golang/prometheus/testutil`.
- `make test` runs `go test -p=1`, and Ginkgo runs specs serially inside the package, so the `DefaultGatherer` is shared across specs; tests must isolate themselves by label value (see requirement 7).
</context>

<requirements>
1. **Bucket function (new file `pkg/github_api_metrics.go`, package `pkg`).** Add an exported pure function

   ```go
   // GitHubAPIBucket returns the GitHub rate-limit bucket a request spends,
   // derived from the request path only. ...
   func GitHubAPIBucket(req *http.Request) string
   ```

   Rules, applied to `req.URL.Path`:
   - prefix `/search/` or prefix `/api/v3/search/` (GitHub Enterprise) → `"search"`
   - exactly `/graphql` or exactly `/api/graphql` → `"graphql"`
   - anything else → `"core"`
   - a nil `req` or nil `req.URL` → `"core"`

   It must never read any response header. The doc comment states why: `X-RateLimit-Resource` belongs to the response and arrives after the call, so deriving the bucket from it (or from any state left by a previous response) misattributes interleaved search and core calls. Define the three bucket values, the two outcome values (`"success"`, `"error"`), the class value `"unclassified"` and the service name `"github-pr-watcher"` as unexported constants in this file.

2. **Collectors (`pkg/metrics.go`).** Add two package-level collectors to the existing `var (...)` block and add both to the existing `prometheus.MustRegister(...)` call in `init()`:
   - `githubAPICallsTotal` — `prometheus.NewCounterVec`, `Name: "github_api_calls_total"`, label names in this exact order: `"service", "credential", "bucket", "outcome", "cluster"`.
   - `githubAPIRateLimitedTotal` — `prometheus.NewCounterVec`, `Name: "github_api_rate_limited_total"`, label names in this exact order: `"service", "credential", "bucket", "class", "cluster"`.

   Use the metric names exactly as written, without the `github_pr_watcher_` prefix: they are shared cross-service names, and the `service` label carries the origin. Write `Help` strings per `RULE go-prometheus/help-string-quality`: what is counted, that one increment happens per HTTP round trip including transport errors (calls), the bucket is derived from the request path, and for rate-limited, the trigger (429, or 403 with `X-RateLimit-Remaining: 0` or a `Retry-After` header) and that `class` is always `unclassified` until a classification rule exists. Do NOT pre-initialise these two in `init()` — their `credential` and `cluster` values are only known at runtime (requirement 3 does it).

3. **Metrics interface and constructor (`pkg/github_api_metrics.go`).** Add, with a counterfeiter annotation in the same style as `pkg/metrics.go`:

   ```go
   //counterfeiter:generate -o ../mocks/github_api_metrics.go --fake-name GitHubAPIMetrics . GitHubAPIMetrics

   // GitHubAPIMetrics records per-caller GitHub API counters. The service,
   // credential and cluster labels are bound at construction.
   type GitHubAPIMetrics interface {
   	// IncGitHubAPICall counts one round trip. outcome: "success", "error".
   	IncGitHubAPICall(bucket string, outcome string)
   	// IncGitHubAPIRateLimited counts one rate-limited response. class: "unclassified".
   	IncGitHubAPIRateLimited(bucket string, class string)
   }

   // NewGitHubAPIMetrics ... credential is the GitHub App id as a decimal
   // string; it must never be a token or any other secret.
   func NewGitHubAPIMetrics(credential string, cluster string) GitHubAPIMetrics
   ```

   The constructor returns an unexported struct holding `credential` and `cluster`, and before returning pre-initialises every bounded combination with `.WithLabelValues(...).Add(0)`: for `githubAPICallsTotal` all 3 buckets × {`success`, `error`} (6 series); for `githubAPIRateLimitedTotal` all 3 buckets × {`unclassified`} (3 series). The two methods call `WithLabelValues(service, credential, bucket, outcome|class, cluster).Inc()` with the label order from requirement 2.

4. **Record at the transport (`pkg/githubclient.go`).**
   - Add a field `metrics GitHubAPIMetrics` to `rateCapturingTransport`. A nil `metrics` disables recording (same convention as the nil `log`), which keeps every existing `&rateCapturingTransport{...}` literal in the internal tests compiling and behaving unchanged.
   - In `RoundTrip`, call a new unexported helper (e.g. `t.recordCall(req, resp, err)`) immediately after `t.inner.RoundTrip(req)` and BEFORE the existing `if err != nil || resp == nil { return resp, err }` early return, so transport errors are counted. Keep `RoundTrip` otherwise identical: `captureRemaining` and the `logNon2xxResponse` call stay where they are and unchanged.
   - The helper: return if `t.metrics == nil`; `bucket := GitHubAPIBucket(req)`; if `err != nil || resp == nil` → `IncGitHubAPICall(bucket, "error")` and return; otherwise outcome is `"success"` for `resp.StatusCode < 400`, else `"error"`, then `IncGitHubAPICall(bucket, outcome)`; then, if the response is rate-limited, `IncGitHubAPIRateLimited(bucket, "unclassified")`.
   - Rate-limited means: `resp.StatusCode == 429`, OR `resp.StatusCode == 403` AND (`resp.Header.Get("X-RateLimit-Remaining") == "0"` OR `resp.Header.Get("Retry-After") != ""`). Put this in its own small unexported predicate in `pkg/githubclient.go`, not in `pkg/github_api_metrics.go` (the `<verification>` `.Header` check scans that whole file). It reads headers only — never the body (the non-2xx logger owns the body peek).
   - The class is always the literal `"unclassified"`. Never omit the label and never derive another class value.
   - Extend the `rateCapturingTransport` doc comment with one short paragraph: the same capture point counts every round trip into `github_api_calls_total` / `github_api_rate_limited_total`, bucket from the request path.

5. **Wire through `NewGitHubClient`.** Change the signature to

   ```go
   func NewGitHubClient(httpClient *http.Client, apiMetrics GitHubAPIMetrics) GitHubClient
   ```

   and set `metrics: apiMetrics` in the `rateCapturingTransport` literal. Update the doc comment to say the wrapped transport also counts every call into the per-caller counters.

6. **Config and `main.go`.**
   - Add to the `application` struct, next to `Stage`, using the same tag layout as the neighbouring fields:
     `Cluster string` with tags `required:"false" arg:"cluster" env:"CLUSTER" usage:"Cluster name stamped on the github_api_* counters' cluster label" default:"nuke"`.
   - In `Run`, immediately before `ghClient := pkg.NewGitHubClient(httpClient)`, construct
     `githubAPIMetrics := pkg.NewGitHubAPIMetrics(strconv.FormatInt(a.AppID, 10), a.Cluster)`
     and pass it: `ghClient := pkg.NewGitHubClient(httpClient, githubAPIMetrics)`. The `credential` label is the numeric App id only — never `PEMKey`, an installation token, or any value derived from them.
   - `NewGitHubClient` has no other caller; confirm with `grep -rn 'NewGitHubClient(' --include='*.go' .` and update any caller found.

7. **Tests (new file `pkg/github_api_metrics_test.go`, `package pkg_test`, in the existing suite — do not add a new suite).**
   - Add accessors to `pkg/metrics_export_test.go` in the existing style: `GitHubAPICallsTotalForTest() *prometheus.CounterVec` and `GitHubAPIRateLimitedTotalForTest() *prometheus.CounterVec`.
   - Isolation: the collectors are process-global, so every spec constructs its own `pkg.NewGitHubAPIMetrics(<credential>, "test-cluster")` with a credential value unique to that spec (e.g. `"test-interleave"`, and per table entry a distinct value). Read series with `testutil.ToFloat64(pkg.GitHubAPICallsTotalForTest().WithLabelValues("github-pr-watcher", cred, bucket, outcome, "test-cluster"))` (label order per requirement 2).
   - Drive requests through the real wiring: create an `httptest.Server`, take `client := srv.Client()`, call `pkg.NewGitHubClient(client, apiMetrics)` (this wraps `client.Transport`), then send requests with `client.Get(srv.URL + "<path>")` and close each response body. If you need a stub `http.RoundTripper` type in `pkg_test`, give it a name that does not collide with any existing identifier in `pkg_test` files (grep first; `roundTripFunc` lives in `package pkg` and is not visible there).

   Cover:
   - **(a) Bucket table** (`DescribeTable` over `pkg.GitHubAPIBucket`): `/search/issues` → search; `/api/v3/search/issues` → search; `/graphql` → graphql; `/api/graphql` → graphql; `/repos/o/r/pulls` → core; `/rate_limit` → core; `/search` (no trailing slash) → core; `/repos/o/search/x` → core; `/graphql/x` → core; a request whose URL carries a query string (`/search/issues?q=x`) → search; nil request → core.
   - **(b) Interleaved inverse test (mandatory).** Against one server and one `apiMetrics`, in this order: GET a search path (`/search/issues`); then GET a core path (`/repos/o/r/pulls`) whose response carries `X-RateLimit-Resource: core`; then GET `/search/issues` again whose response carries `X-RateLimit-Resource: search`. All three respond 200. Assert `calls_total{bucket="search",outcome="success"} == 2`, `{bucket="core",outcome="success"} == 1`, `{bucket="graphql",outcome="success"} == 0`, and every `outcome="error"` series for this credential is 0. The handler may choose the header per request by path and call order; it must not influence the bucket the client records.
   - **(c) Status table** (each entry its own credential): `429` on `/repos/o/r/pulls` → core `error` +1 and `rate_limited{bucket="core",class="unclassified"}` +1; `403` with `X-RateLimit-Remaining: 0` on `/repos/o/r/pulls` → core `error` +1 and `rate_limited{bucket="core",class="unclassified"}` +1; `403` with `Retry-After: 60` on `/search/issues` → search `error` +1 and `rate_limited{bucket="search",class="unclassified"}` +1; `403` with `X-RateLimit-Remaining: 4999` and no `Retry-After` → `error` +1, rate_limited 0; `404` → `error` +1, rate_limited 0; `200` → `success` +1, rate_limited 0. In every entry the opposite outcome series stays 0.
   - **(d) Transport error.** Build an `*http.Client` whose `Transport` is a stub returning `(nil, <error>)`, wrap it with `pkg.NewGitHubClient(client, apiMetrics)`, and `client.Get("https://api.github.com/search/issues")` → the call returns an error; `calls_total{bucket="search",outcome="error"} == 1`, `{bucket="search",outcome="success"} == 0`, and `rate_limited{bucket="search",class="unclassified"} == 0`.
   - **(e) Registration and pre-initialisation.** Record `before := testutil.GatherAndCount(prometheus.DefaultGatherer, "github_api_calls_total")` (and the same for `github_api_rate_limited_total`), construct `pkg.NewGitHubAPIMetrics` with a fresh credential, and assert the counts grew by exactly 6 and 3 respectively — this proves both the `init()` registration (it reads the `DefaultGatherer`, not the accessor) and the pre-initialisation. Also assert each of those 9 series reads 0 via `testutil.ToFloat64`.

8. **Do not change** the `rate_limit_remaining` gauge path (`captureRemaining`, its core-only scoping, its nil/malformed handling) or the non-2xx log line (`logNon2xxResponse`, `non2xxLine`, `logNon2xxV2`). The existing tests in `pkg/rate_capture_test.go` and `pkg/non2xx_log_test.go` must pass without edits.

9. **README.md.**
   - Add a `CLUSTER` row to the `## Environment Variables` table: not required, default `nuke`, "Cluster name stamped on the `cluster` label of the `github_api_*` counters".
   - Add two rows to the `## Metrics` table for `github_api_calls_total` and `github_api_rate_limited_total` (type counter, their labels, one-line meaning, bucket from the request path, `class` currently always `unclassified`), and amend the intro sentence so it no longer claims every metric carries the `github_pr_watcher_` prefix (the two `github_api_*` counters are shared cross-service names distinguished by `service`).

10. **CHANGELOG.md.** The file has no `## Unreleased` section; add `## Unreleased` directly above `## v0.19.0` (separated by one blank line on each side) containing exactly this bullet:

    `- feat: add github_api_calls_total and github_api_rate_limited_total per-caller counters with request-derived bucket`

11. **Self-check before finishing.** Re-run every `<verification>` command and confirm each prints the stated result. Then walk these against the code: transport errors are counted (the record call sits before the early return); the bucket function reads no response header; `class` is only ever `"unclassified"`; all 6 + 3 combinations are pre-initialised in the constructor; no label value is derived from a token or the PEM key; the existing gauge and log tests are untouched and pass.
</requirements>

<constraints>
- The bucket comes from the REQUEST path only — never from `X-RateLimit-Resource` or any other response header, and never from state left by a previous response.
- `outcome`: `success` for status < 400, `error` for status >= 400 and for any transport error (`err != nil` or `resp == nil`).
- Rate-limited: status 429, or 403 with `X-RateLimit-Remaining: 0` or a non-empty `Retry-After` header. `class` is always the literal `unclassified`; never omit the label, never guess another value.
- Labels: `service` = constant `github-pr-watcher`; `credential` = the GitHub App id as a decimal string; `cluster` = config `CLUSTER` (default `nuke`). Never put a token, PEM key or any secret into a label.
- Pre-initialise every bounded combination at construction with `.Add(0)` (guide rule `go-prometheus/counter-pre-initialization`).
- Do not change the existing `github_pr_watcher_rate_limit_remaining` gauge behaviour or the non-2xx log line.
- No new dependency; Prometheus client is already a direct dependency.
- Errors (if any are introduced) use `github.com/bborbe/errors`; no `fmt.Errorf` in production code.
- Tests use Ginkgo v2 / Gomega in the existing `pkg_test` suite; mocks via counterfeiter into `mocks/` (regenerated by `make precommit`'s `generate` step).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must exit 0 (format, generate, tests, lint, license).

Then confirm the wiring (each must succeed):

```bash
grep -q 'func GitHubAPIBucket(req \*http.Request) string' pkg/github_api_metrics.go
grep -q 'func NewGitHubAPIMetrics(credential string, cluster string) GitHubAPIMetrics' pkg/github_api_metrics.go
grep -q 'func NewGitHubClient(httpClient \*http.Client, apiMetrics GitHubAPIMetrics) GitHubClient' pkg/githubclient.go
grep -q '"github_api_calls_total"' pkg/metrics.go
grep -q '"github_api_rate_limited_total"' pkg/metrics.go
grep -q 'pkg.NewGitHubAPIMetrics(strconv.FormatInt(a.AppID, 10), a.Cluster)' main.go
grep -q 'env:"CLUSTER"' main.go
test -f mocks/github_api_metrics.go
! grep -vE '^[[:space:]]*//' pkg/github_api_metrics.go | grep -q '\.Header'
awk '/^## /{sec=$0} /github_api_calls_total and github_api_rate_limited_total per-caller counters/{print sec}' CHANGELOG.md | grep -qx '## Unreleased'
```
</verification>
