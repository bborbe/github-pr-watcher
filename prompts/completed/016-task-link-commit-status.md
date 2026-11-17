---
status: completed
summary: 'Added the task-link commit-status path mirroring do-not-merge: command, predicate, sender, executor, webhook wiring, factory/main wiring, tests, mock, and changelog.'
execution_id: github-pr-watcher-task-link-status-exec-016-task-link-commit-status
dark-factory-version: v0.199.1
created: "2026-11-17T10:59:31Z"
queued: "2026-11-17T11:05:00Z"
started: "2026-11-17T11:06:27Z"
completed: "2026-11-17T11:14:16Z"
---

<summary>
- The watcher now reports the `task-link` merge check itself, so repos no longer need a GitHub Actions run per PR event to produce it.
- A PR passes when its description has a line of its own reading `Task: <lowercase task UUID>`; anything else, including an empty description, fails.
- The pass/fail rule is the rule the retired per-repo workflow applied (ASCII whitespace only), so a normally formatted PR description gets the same result as before.
- The check is re-evaluated when a PR is opened, reopened, pushed to, marked ready for review, and when its description is edited — so fixing the description turns the check green, and breaking it after a pass turns it red.
- Editing a PR description does NOT start a code review; it only re-evaluates the check.
- A permanently failing post (for example a missing GitHub App permission) is logged loudly and not retried forever; transient GitHub errors are retried.
- The existing do-not-merge gate and review dispatch behave exactly as before.
</summary>

<objective>
Post a `task-link` commit status on every PR head SHA from this service, as an exact mirror of the existing `do-not-merge` commit-status path (webhook publishes a command, the in-pod command consumer posts the status). The repos' master rulesets require the context `task-link` by name only, so a status posted by this service satisfies them unchanged and the per-repo `check-task-link.yml` workflow (which burns the private Actions allowance) can later be deleted.
</objective>

<context>
Read `CLAUDE.md` if present and `docs/dod.md`.

Read these files in full before changing anything — every new piece mirrors one of them:

- `pkg/command/set_do_not_merge_status_command.go` — `SetDoNotMergeStatusCommandOperation`, `DoNotMergeStatusContext`, `SetDoNotMergeStatusCommand` (fields `URL`, `HeadSHA`, `Labels`), `Blocked()`, the shared predicate `hasDoNotMergeLabel`, and `Validate(ctx)` built on `validation.All` + `prurl.ParsePRURL`.
- `pkg/command/set_do_not_merge_status_executor.go` — `NewSetDoNotMergeStatusCommandExecutor(ghClient pkg.GitHubClient) cdb.CommandObjectExecutorTx` and `runSetDoNotMergeStatus`, including the exit-path mapping and the loud `glog.Errorf` on `pkg.ErrPermanent`.
- `pkg/command/set_do_not_merge_status_command_sender.go` — the `//counterfeiter:generate` directive, the `SetDoNotMergeStatusCommandSender` interface, `NewSetDoNotMergeStatusCommandSender(commandCreator base.CommandCreator, initiator cqrsiam.Initiator, commandObjectSender cdb.CommandObjectSender)`, and `SendCommand` (Validate → `base.ParseEvent` → `cdb.CommandObject` with `SchemaID: lib.GithubPRReviewV1SchemaID`).
- `pkg/command/set_do_not_merge_status_command_test.go`, `pkg/command/set_do_not_merge_status_executor_test.go`, `pkg/command/suite_test.go` (the `//go:generate ... counterfeiter ... -generate` line). Note `validPRURL` is a package-level const declared in `pkg/command/trigger_pr_review_executor_test.go` and is already visible to all `command_test` files; the helpers `mustParseStatusEvent` / `newStatusCommandObject` are typed to the do-not-merge command, so the new test needs its own differently named helpers.
- `pkg/handler/webhook_handler.go` — `NewWebhookHandler`, the `webhookHandler` struct, `dispatchActions`, `gateActions`, `ServeHTTP` (the `postDoNotMergeGate` call sits BEFORE the `dispatchActions` check and before `shouldDispatch`/the coalescer), `postDoNotMergeGate`, `sendDoNotMergeStatus`, and the `webhookPullRequestEvent` struct.
- `pkg/handler/webhook_handler_test.go` — the `BeforeEach` that builds the handler, the two extra `handler.NewWebhookHandler(...)` call sites (the "fail closed" case and the "trailing coalesce" `BeforeEach`), `pullRequestPayload`, `labeledPayload`, `testHeadSHA`, and the `Context("do-not-merge commit status", ...)` block.
- `pkg/factory/factory.go` — `CreateSetDoNotMergeStatusCommandSender` and the `executors` list inside `CreateCommandConsumer`.
- `pkg/factory/webhook.go` — `CreateWebhookHandler`.
- `main.go` — the `factory.CreateWebhookHandler(...)` call (it passes `factory.CreateSetDoNotMergeStatusCommandSender(ctx, syncProducer, a.TopicPrefix)`). `main.go` is the only binary entry point in this repo (there is no `cmd/` directory).
- `pkg/githubclient.go` — `GitHubClient.PostCommitStatus(ctx context.Context, owner, repo, sha string, state CommitState, statusContext string) error`, `CommitStateSuccess` / `CommitStateFailure`, and `ErrPermanent`.
- `.github/workflows/check-task-link.yml` — the workflow whose predicate this change reproduces. Read it; do NOT modify or delete it.

Coding-guide references (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. **Command — new file `pkg/command/set_task_link_status_command.go`.** Mirror `set_do_not_merge_status_command.go`:
   - `const SetTaskLinkStatusCommandOperation base.CommandOperation = "set-task-link-status"` with a doc comment stating it rides the existing `GithubPRReviewV1SchemaID` topic trio (consumer dispatches by operation; no new schema, no `bborbe/maintainer` change). The value must satisfy the cqrs operation format `^[a-z][a-z-]*$` (lowercase, hyphens only).
   - `const TaskLinkStatusContext = "task-link"` with a comment that the master-protection rulesets require this context by name, so renaming it silently breaks every merge in those repos.
   - `type SetTaskLinkStatusCommand struct` with fields `URL string \`json:"url"\``, `HeadSHA string \`json:"head_sha"\``, `Body string \`json:"body,omitempty"\`` and doc comments (Body is the PR description at delivery time; the executor derives the state from it rather than re-reading the PR, same reasoning as the do-not-merge label set).
   - Method `func (cmd SetTaskLinkStatusCommand) Linked() bool` returning `HasTaskLinkLine(cmd.Body)`.
   - Method `func (cmd SetTaskLinkStatusCommand) Validate(ctx context.Context) error` with exactly the same URL and HeadSHA rules as `SetDoNotMergeStatusCommand.Validate`. Body is NOT validated: an empty body is a legitimate input that must produce a `failure` status, so rejecting it would leave a previously green PR green after its description was cleared.

2. **Predicate — `HasTaskLinkLine(body string) bool`** in the same file, as the single function that decides pass/fail. It MUST be byte-equivalent to the workflow's `printf '%s\n' "$PR_BODY" | grep -Eq '<ERE>'`:
   - Compile once into a package-level variable (import `regexp` from the standard library; it is RE2, linear-time, so an untrusted body cannot cause catastrophic backtracking):
     ```go
     var taskLinkLineRegexp = regexp.MustCompile(
     	`^[[:space:]]*Task:[[:space:]]*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[[:space:]]*$`,
     )
     ```
   - Split `body` with `strings.Split(body, "\n")`, strip one trailing `"\r"` from each line with `strings.TrimSuffix(line, "\r")`, and return true when ANY single line matches the regexp. Empty body → false.
   - Match per line, never against the whole body with `(?m)`: `[[:space:]]` also matches `\n`, so a whole-body multiline match would let `Task:` and the UUID sit on different lines, which `grep` (line-based) rejects.
   - Key `Task:` is case-sensitive, hex is lowercase only, the UUID must be the whole remainder of the line (trailing whitespace only).
   - Do not write the literal multiline-flag token (paren, question mark, m, paren) anywhere in the file, including comments — the verification greps for its absence. Describe it in words if needed.
   - Doc comment: quote the ERE and name `.github/workflows/check-task-link.yml` as the predicate this must stay equivalent to.

3. **Command tests — new file `pkg/command/set_task_link_status_command_test.go`** (package `command_test`, Ginkgo v2 / Gomega):
   - A `DescribeTable` on `command.HasTaskLinkLine` using the UUID `0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0` (lowercase), with at least these entries and expected results:
     - `"Task: <uuid>"` → true
     - `"   Task:   <uuid>   "` (leading/trailing spaces) → true
     - `"\tTask:\t<uuid>\t"` (tabs) → true
     - `"Task:<uuid>"` (no space after the colon) → true
     - `"Task: 0B1C2D3E-4F50-6172-8394-A5B6C7D8E9F0"` (uppercase hex) → false
     - `"task: <uuid>"` (lowercase key) → false
     - `"subtask: <uuid>"` → false
     - `"not a task: <uuid>"` → false
     - `"- Task: <uuid>"` (markdown bullet) → false
     - `"Task: <uuid> extra"` (UUID followed by text) → false
     - `"Task: <uuid>x"` → false
     - `"Summary line\r\n\r\nTask: <uuid>\r\nMore text\r\n"` (CRLF, match in the middle) → true
     - `"Summary\n\nTask: <uuid>\nfooter"` (LF, match in the middle) → true
     - `"Task:\n<uuid>"` (key and UUID on different lines) → false
     - `""` (empty body) → false
     - `"Task: "` (no UUID) → false
   - `Describe("Linked")`: true for a command whose Body holds a valid line, false for an empty Body.
   - `Describe("Validate")`: mirror the five cases in `set_do_not_merge_status_command_test.go` (valid URL + SHA succeeds; empty URL, unparseable URL, non-github URL, empty HeadSHA each fail), plus: a valid URL + SHA with an EMPTY Body succeeds.
   - A boundary test that `command.SetTaskLinkStatusCommandOperation.Validate(ctx)` succeeds. `base.CommandOperation` has `func (c CommandOperation) Validate(ctx context.Context) error` (`github.com/bborbe/cqrs` v0.6.15, `base/base_command-operation.go`), which enforces the operation-name format at runtime — the compiler does not.

4. **Sender — new file `pkg/command/set_task_link_status_command_sender.go`.** Copy the shape of `set_do_not_merge_status_command_sender.go` exactly:
   - `//counterfeiter:generate -o ../../mocks/set_task_link_status_command_sender.go --fake-name SetTaskLinkStatusCommandSender . SetTaskLinkStatusCommandSender`
   - `type SetTaskLinkStatusCommandSender interface { SendCommand(ctx context.Context, cmd SetTaskLinkStatusCommand) error }`
   - `func NewSetTaskLinkStatusCommandSender(commandCreator base.CommandCreator, initiator cqrsiam.Initiator, commandObjectSender cdb.CommandObjectSender) SetTaskLinkStatusCommandSender`
   - `SendCommand`: Validate → `base.ParseEvent` → `cdb.CommandObject{Command: s.commandCreator.NewCommand(SetTaskLinkStatusCommandOperation, s.initiator, "", event), SchemaID: lib.GithubPRReviewV1SchemaID}` → `SendCommandObject`; same `errors.Wrapf` messages with the new type name; `glog.V(2)` log line with op, url, sha and `len(cmd.Body)` — never log the body content.

5. **Executor — new file `pkg/command/set_task_link_status_executor.go`.** Mirror `set_do_not_merge_status_executor.go`. Also reword the `CommitStateSuccess` / `CommitStateFailure` doc comments in `pkg/githubclient.go` to be gate-neutral (the check passes / fails), since they now serve both do-not-merge and task-link:
   - `func NewSetTaskLinkStatusCommandExecutor(ghClient pkg.GitHubClient) cdb.CommandObjectExecutorTx` using `cdb.CommandObjectExecutorTxFunc(SetTaskLinkStatusCommandOperation, false, ...)` delegating to an unexported `runSetTaskLinkStatus`.
   - Exit-path mapping (identical to do-not-merge, document it in the doc comment):
     - `commandObject.Command.Data.MarshalInto` fails → `errors.Wrapf(ctx, cdb.ErrCommandObjectSkipped, "malformed SetTaskLinkStatusCommand: %v", err)`
     - `cmd.Validate` fails → wrapped `cdb.ErrCommandObjectSkipped`
     - `prurl.ParsePRURL` fails → wrapped `cdb.ErrCommandObjectSkipped`
     - state = `pkg.CommitStateSuccess` when `cmd.Linked()`, else `pkg.CommitStateFailure`
     - `ghClient.PostCommitStatus(ctx, prInfo.Owner, prInfo.Repo, cmd.HeadSHA, state, TaskLinkStatusContext)`:
       - `errors.Is(err, pkg.ErrPermanent)` → `glog.Errorf("task-link status permanently failed, not retrying url=%s sha=%s: %v", ...)` with a comment explaining that a missing required `task-link` context blocks every merge in the repos that require it, then return wrapped `cdb.ErrCommandObjectSkipped`
       - any other error → `errors.Wrapf(ctx, err, "post %s status on %s@%s", TaskLinkStatusContext, cmd.URL, cmd.HeadSHA)` (retried)
     - success → `glog.V(2).Infof("task-link status posted url=%s sha=%s state=%s", ...)`, return `nil, nil, nil`.

5b. **Sender tests — new file `pkg/command/set_task_link_status_command_sender_test.go`.** Mirror `pkg/command/trigger_pr_review_command_sender_test.go` (reuse its `newTestCommandCreator`, use `cdbmocks.CDBCommandObjectSender`). Cases: valid command → one `SendCommandObject` call, `obj.SchemaID == lib.GithubPRReviewV1SchemaID`, `obj.Command.Operation == command.SetTaskLinkStatusCommandOperation`; invalid command (empty URL) → error containing `validate SetTaskLinkStatusCommand`, zero sends; Kafka send error → wrapped error. Round-trip: take the captured `CommandObject` for a command whose Body holds `"Summary\r\n\r\nTask: 0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0\r\n"`, feed it to `command.NewSetTaskLinkStatusCommandExecutor(ghClient).HandleCommand(ctx, nil, obj)`, and assert one `PostCommitStatus` with `pkg.CommitStateSuccess` — this proves the producer-side payload survives the consumer's `MarshalInto`.

6. **Executor tests — new file `pkg/command/set_task_link_status_executor_test.go`.** Mirror `set_do_not_merge_status_executor_test.go` with new helper names (`mustParseTaskLinkEvent`, `newTaskLinkCommandObject`), a `*mocks.GitHubClient`, and `executor.HandleCommand(ctx, nil, ...)`. Cases:
   - body with a valid `Task: <uuid>` line → exactly one `PostCommitStatus` call; owner `bborbe`, repo `repo`, sha `abc123`, state `pkg.CommitStateSuccess`, context `command.TaskLinkStatusContext`.
   - body without a task line → state `pkg.CommitStateFailure`.
   - empty body → state `pkg.CommitStateFailure` (posted, not skipped).
   - `PostCommitStatusReturns(errors.Wrapf(ctx, pkg.ErrPermanent, "403 permission denied"))` → error, `errors.Is(err, cdb.ErrCommandObjectSkipped)` true.
   - `PostCommitStatusReturns(errors.Errorf(ctx, "502 bad gateway"))` → error, `errors.Is(err, cdb.ErrCommandObjectSkipped)` false.
   - URL `not-a-url` → skipped, zero `PostCommitStatus` calls.
   - empty HeadSHA → skipped, zero `PostCommitStatus` calls.
   - malformed payload: a `cdb.CommandObject` whose `Command.Data` cannot be marshalled into the struct (for example an event carrying `"head_sha": 42`, built with `base.ParseEvent` from a `map[string]any`) → skipped, zero `PostCommitStatus` calls.

7. **Webhook payload.** In `pkg/handler/webhook_handler.go`, add `Body string \`json:"body"\`` to the `PullRequest` struct inside `webhookPullRequestEvent`, and update the struct's doc comment to say Body is read only to derive the task-link status. GitHub sends `"body": null` for a PR with no description; `encoding/json` leaves a `string` field at `""` for `null`, so no pointer type is needed.

8. **Webhook action set.** Add, next to `gateActions`, a separate set:
   ```go
   var taskLinkActions = map[string]bool{
   	"opened":           true,
   	"reopened":         true,
   	"synchronize":      true,
   	"ready_for_review": true,
   	"edited":           true,
   }
   ```
   with a doc comment explaining: the PR body is the check's only input, so `edited` is load-bearing (without it a body fix never re-evaluates and a body broken after passing stays green); this set is deliberately separate from `dispatchActions` (an edit must not trigger a full review) and from `gateActions` (an edit cannot change labels, and adding `edited` there would publish a needless do-not-merge status and CHANGES_REQUESTED-review command on every title/body edit); and `labeled`/`unlabeled` are absent because they cannot change the body. Do NOT add `edited` to `dispatchActions` or `gateActions`. Update the `dispatchActions` comment so it no longer implies `edited` is dropped entirely (it is dropped for review dispatch, but still re-evaluates the task-link status).

9. **Webhook handler wiring.**
   - Add a field `taskLinkSender command.SetTaskLinkStatusCommandSender` to `webhookHandler`, and a parameter `taskLinkSender command.SetTaskLinkStatusCommandSender` to `NewWebhookHandler` placed directly after `reviewSender`. Update the doc comment of `NewWebhookHandler`.
   - Add `func (h *webhookHandler) postTaskLinkGate(ctx context.Context, payload webhookPullRequestEvent) error` mirroring `postDoNotMergeGate`: no-op when `!taskLinkActions[payload.Action]`; otherwise call `sendTaskLinkStatus` and on error return `libhttp.WrapWithStatusCode(errors.Wrap(ctx, err, "send SetTaskLinkStatusCommand"), http.StatusBadGateway)`.
   - Add `func (h *webhookHandler) sendTaskLinkStatus(ctx context.Context, payload webhookPullRequestEvent) error` mirroring `sendDoNotMergeStatus`: same empty `html_url`/`head.sha` guard returning `errors.Errorf`, publish `command.SetTaskLinkStatusCommand{URL: payload.PullRequest.HTMLURL, HeadSHA: payload.PullRequest.Head.SHA, Body: payload.PullRequest.Body}`, wrap a send error, `glog.V(2)` log with url, sha, action and `len(payload.PullRequest.Body)` (never the body text).
   - In `ServeHTTP`, call `h.postTaskLinkGate(ctx, payload)` immediately after the existing `postDoNotMergeGate` call and before the `dispatchActions` check, returning its error unchanged. Extend the comment above `postDoNotMergeGate` (or add one above the new call) explaining the task-link status is published outside the coalescer for the same reason: every head SHA the ruleset evaluates needs its own status. With this placement an `edited` delivery publishes the task-link command and then falls through to the existing non-dispatch branch (`IncWebhookDelivery("skip")`, 200).

10. **Factory and main wiring.**
    - `pkg/factory/factory.go`: add `func CreateSetTaskLinkStatusCommandSender(ctx context.Context, syncProducer libkafka.SyncProducer, topicPrefix base.TopicPrefix) command.SetTaskLinkStatusCommandSender` directly after `CreateSetDoNotMergeStatusCommandSender`, same body shape (`base.NewCommandCreator(base.RequestIDChannel(ctx))`, `cqrsiam.Initiator("watcher-github-pr")`, `cdb.NewCommandObjectSender(syncProducer, topicPrefix, log.DefaultSamplerFactory)`), with a doc comment. In `CreateCommandConsumer`, append `command.NewSetTaskLinkStatusCommandExecutor(ghClient)` to the `executors` list after the do-not-merge executors, with a comment that it replaces the per-repo `check-task-link` workflows and rides the same topic trio.
    - `pkg/factory/webhook.go`: add the `taskLinkSender command.SetTaskLinkStatusCommandSender` parameter to `CreateWebhookHandler` (directly after `reviewSender`) and pass it through to `handler.NewWebhookHandler`; update the doc comment.
    - `main.go`: in the `factory.CreateWebhookHandler(...)` call, pass `factory.CreateSetTaskLinkStatusCommandSender(ctx, syncProducer, a.TopicPrefix)` directly after the `CreateSetDoNotMergeReviewCommandSender` argument; extend the comment above the call to mention the task-link status.
    - Every caller of `NewWebhookHandler` / `CreateWebhookHandler` must compile: `main.go`, `pkg/factory/webhook.go`, and the three `handler.NewWebhookHandler(` call sites in `pkg/handler/webhook_handler_test.go`. Run `grep -rn 'NewWebhookHandler(\|CreateWebhookHandler(' --include='*.go' . | grep -v '^./vendor/'` to confirm none were missed.

11. **Mocks.** `make precommit` runs `go generate -mod=mod ./...`, which regenerates counterfeiter mocks from the `//counterfeiter:generate` directive into `mocks/set_task_link_status_command_sender.go` (fake name `SetTaskLinkStatusCommandSender`). Run `go generate -mod=mod ./pkg/command/...` before writing the handler tests so the fake exists; do not hand-write the mock.

12. **Webhook handler tests** in `pkg/handler/webhook_handler_test.go`:
    - Add `taskLinkSender *mocks.SetTaskLinkStatusCommandSender` to the `var` block, create it in `BeforeEach`, and pass it to `handler.NewWebhookHandler`. Pass `new(mocks.SetTaskLinkStatusCommandSender)` at the other two call sites.
    - Add a payload helper that builds the JSON with `encoding/json` (`json.Marshal` of a `map[string]any` with `action`, and `pull_request` carrying `html_url` `https://github.com/bborbe/repo/pull/42`, `head.sha` = `testHeadSHA`, and `body`), so bodies with `\n`/`\r\n` are escaped correctly.
    - New `Context("task-link commit status", ...)`:
      - A `DescribeTable` over `opened`, `reopened`, `synchronize`, `ready_for_review`, `edited`: delivery with body `"Summary\r\n\r\nTask: 0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0\r\n"` → `taskLinkSender.SendCommandCallCount()` is 1 and the sent command's `URL`, `HeadSHA` (`testHeadSHA`) and `Body` (exactly the sent body) match.
      - `edited` → response 200, `sender.SendCommandCallCount()` 0 (no review dispatch), `statusSender` and `reviewSender` call counts 0, `metrics.IncWebhookDeliveryArgsForCall(0)` is `"skip"`, `taskLinkSender` call count 1.
      - `labeled` (via `labeledPayload`) and `closed` → `taskLinkSender.SendCommandCallCount()` 0.
      - `"body": null` on `opened` → command sent with `Body` `""`.
      - `taskLinkSender.SendCommandReturns(errors.Errorf(ctx, "kafka down"))` on `opened` → 502 and `sender.SendCommandCallCount()` 0.
    - Extend the existing "does not post the status on a delivery that cannot change it" case to also assert `taskLinkSender.SendCommandCallCount()` is 0.

13. **CHANGELOG.md.** The file currently has no `## Unreleased` heading. Add `## Unreleased` directly above `## v0.28.3` (one blank line on each side), containing exactly one bullet:
    `- feat: post the \`task-link\` commit status from the watcher on opened, reopened, synchronize, ready_for_review and edited deliveries, using the same predicate as the \`check-task-link\` workflow, so repos can drop that per-PR Actions run`
    There must be exactly one `## Unreleased` heading in the file.

14. **Self-check before finishing.** Re-run every `<verification>` command and confirm each passes. Then walk these against the code: the regexp string is byte-identical to the one in `.github/workflows/check-task-link.yml`; matching is per line (no `(?m)`); `Validate` does not reject an empty body; `edited` appears only in `taskLinkActions`; the task-link publish happens before `shouldDispatch`; the body text is never logged; the executor maps `ErrPermanent` to a skip with `glog.Errorf` and everything else to a wrapped retryable error.
</requirements>

<constraints>
- Exact mirror of the do-not-merge commit-status path: same topic trio (`lib.GithubPRReviewV1SchemaID`), same initiator, same exit-path mapping, same webhook placement (before the coalescer), same 502 on send failure.
- The status context string is exactly `task-link` — the rulesets require it by name.
- The predicate must be equivalent to the workflow's ERE `^[[:space:]]*Task:[[:space:]]*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[[:space:]]*$`, evaluated per line; lowercase hex only; case-sensitive `Task:` key; empty body fails.
- `edited` must NOT be added to `dispatchActions` (no review on an edit) or to `gateActions`.
- Never log the PR body content; log its length only.
- Out of scope: deleting `check-task-link.yml` (here or in any of the nine repos), modifying anything under `.github/workflows/`, any ruleset change, any deployment, README changes, a new Kafka schema or any `bborbe/maintainer` change, new Prometheus metrics, new config fields or flags.
- Errors use `github.com/bborbe/errors` (`errors.Wrap`, `errors.Wrapf`, `errors.Errorf`); no `fmt.Errorf` in production code.
- Tests use Ginkgo v2 / Gomega in the existing `command_test` and `handler_test` suites; mocks via counterfeiter into `mocks/` (regenerated by the `generate` step).
- Copyright header on every new file, matching existing files (`// Copyright (c) 2026 Benjamin Borbe All rights reserved.` …).
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
</constraints>

<verification>
Run `ROOTDIR=/workspace make precommit` — must exit 0 (format, generate, tests, lint, license).

Then confirm the wiring (each command must succeed):

```bash
set -e
grep -q 'SetTaskLinkStatusCommandOperation base.CommandOperation = "set-task-link-status"' pkg/command/set_task_link_status_command.go
grep -q 'TaskLinkStatusContext = "task-link"' pkg/command/set_task_link_status_command.go
grep -qF '^[[:space:]]*Task:[[:space:]]*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[[:space:]]*$' pkg/command/set_task_link_status_command.go
! grep -qF '(?m)' pkg/command/set_task_link_status_command.go
grep -q 'func HasTaskLinkLine(body string) bool' pkg/command/set_task_link_status_command.go
grep -q 'func NewSetTaskLinkStatusCommandExecutor(' pkg/command/set_task_link_status_executor.go
test -f mocks/set_task_link_status_command_sender.go
grep -q 'command.NewSetTaskLinkStatusCommandExecutor(ghClient)' pkg/factory/factory.go
grep -q 'func CreateSetTaskLinkStatusCommandSender(' pkg/factory/factory.go
grep -q 'factory.CreateSetTaskLinkStatusCommandSender(ctx, syncProducer, a.TopicPrefix)' main.go
grep -q 'json:"body"' pkg/handler/webhook_handler.go
awk '/^var taskLinkActions/,/^}/' pkg/handler/webhook_handler.go | grep -q '"edited"'
! awk '/^var dispatchActions/,/^}/' pkg/handler/webhook_handler.go | grep -q '"edited"'
! awk '/^var gateActions/,/^}/' pkg/handler/webhook_handler.go | grep -q '"edited"'
test -f .github/workflows/check-task-link.yml
test "$(grep -c '^## Unreleased$' CHANGELOG.md)" = "1"
awk '/^## /{sec=$0} /task-link. commit status from the watcher/{print sec}' CHANGELOG.md | grep -qx '## Unreleased'
```
</verification>
