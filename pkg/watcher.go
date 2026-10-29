// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"slices"
	"strings"

	agentlib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/reviewignore"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

//counterfeiter:generate -o ../mocks/watcher.go --fake-name Watcher . Watcher

// Watcher polls GitHub and publishes task commands to Kafka.
type Watcher interface {
	Poll(ctx context.Context) error
}

// TaskConfig groups the per-task publishing configuration.
type TaskConfig struct {
	Stage       string
	MaxSlugLen  int
	MaxTitleLen int
	TaskSuffix  string
	// TargetVault routes the CreateTaskCommand to a specific vault controller
	// (matched verbatim against the controller's VAULT_NAME). Empty leaves
	// TargetVault unset, so the controller's legacy default-vault fallback
	// applies — preserving pre-TARGET_VAULT behaviour.
	TargetVault string
	// MaxAdditions parks PRs with more than this many added lines at
	// human_review instead of spawning a review pod. 0 disables the check.
	MaxAdditions int
	// MaxChangedFiles parks PRs touching more than this many files at
	// human_review instead of spawning a review pod. 0 disables the check.
	MaxChangedFiles int
}

//counterfeiter:generate -o ../mocks/task_publisher.go --fake-name TaskPublisher . TaskPublisher

// TaskPublisher publishes create-task commands for a given PR + details pair.
// Returns true on successful publish, false on trust check failure or send failure.
type TaskPublisher interface {
	PublishCreate(ctx context.Context, pr PullRequest, taskIDStr string, details PRDetails) bool
	// PublishOverride publishes a `pr-override` task for a labeled PR. It is
	// trusted-authors-only. Returns (handled, emitted):
	//   - handled=true: this is a trusted-author override case; the caller must
	//     NOT emit a review task for this SHA (even when the send failed —
	//     retry on the next poll). This preserves the override-only invariant
	//     that prevents an APPROVE-vs-CHANGES_REQUESTED race.
	//   - emitted=true: the override task was actually sent; the caller records
	//     it in the cursor so the override fires at most once per head SHA.
	//   - untrusted author (or trust-check error) → (false, false): the caller
	//     falls through to the normal review path (which routes untrusted →
	//     human_review).
	PublishOverride(
		ctx context.Context,
		pr PullRequest,
		taskIDStr string,
		details PRDetails,
	) (handled, emitted bool)
	// PublishMechanicalOverride publishes the mechanical `pr-override` task for
	// a qualifying trivial bot bump. The caller (the watcher's mechanical gate)
	// has already evaluated the author list, trust, the repo's two opt-in flags
	// and the file classifier, so this method performs no trust check of its
	// own. Returns true when the command was sent; false when the send failed,
	// in which case the caller records nothing so the emission is retried on
	// the next poll.
	PublishMechanicalOverride(
		ctx context.Context,
		pr PullRequest,
		taskIDStr string,
		details PRDetails,
	) bool
}

// TaskPublisherOption configures optional TaskPublisher behaviour.
type TaskPublisherOption func(*taskPublisher)

// WithPublishRecorder makes the publisher record every review task it sends in
// the retry guard's ledger, so a later forced retry for the same head SHA is
// suppressed while that review is still in flight. Without it the publisher
// records nothing.
func WithPublishRecorder(recorder PublishRecorder) TaskPublisherOption {
	return func(p *taskPublisher) {
		p.recorder = recorder
	}
}

// NewTaskPublisher returns a TaskPublisher that performs trust evaluation
// then publishes a CreateTaskCommand via the given CreateCommandSender.
func NewTaskPublisher(
	createSender task.CreateCommandSender,
	trustDecision trust.Trust,
	metrics Metrics,
	cfg TaskConfig,
	ghClient GitHubClient,
	opts ...TaskPublisherOption,
) TaskPublisher {
	p := &taskPublisher{
		createSender:  createSender,
		trustDecision: trustDecision,
		metrics:       metrics,
		cfg:           cfg,
		ghClient:      ghClient,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

type taskPublisher struct {
	createSender  task.CreateCommandSender
	trustDecision trust.Trust
	metrics       Metrics
	cfg           TaskConfig
	// ghClient reads the repo's `.reviewignore` so the size gate counts only
	// reviewable content. Nil-tolerant: a publisher built without one simply
	// excludes nothing.
	ghClient GitHubClient
	// recorder writes the retry guard's ledger after every successful publish.
	// Nil-tolerant: a publisher built without one records nothing, which is the
	// pre-guard behaviour.
	recorder PublishRecorder
}

// PublishCreate implements TaskPublisher.
func (p *taskPublisher) PublishCreate(
	ctx context.Context,
	pr PullRequest,
	taskIDStr string,
	details PRDetails,
) bool {
	author := pr.AuthorLogin

	trustResult, err := p.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      author,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		glog.Errorf("trust check failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return false
	}

	exclusion := ReviewIgnoreExclusion(ctx, p.ghClient, pr.Owner, pr.Repo, pr.Number, pr.HTMLURL)

	cmd := BuildCreateCommand(
		pr,
		details,
		taskIDStr,
		p.cfg.Stage,
		p.cfg.MaxSlugLen,
		p.cfg.MaxTitleLen,
		p.cfg.TaskSuffix,
		p.cfg.TargetVault,
		trustResult,
		false, // poll path is never a forced re-review
		p.cfg.MaxAdditions,
		p.cfg.MaxChangedFiles,
		exclusion,
	)

	if err := p.createSender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf("publish create-task failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return false
	}
	// Count oversized trusted-author PRs the poll parked at human_review
	// separately from normal reviews — matches BuildCreateCommand's decision
	// (park only applies to trusted authors in the poll path, where forced is
	// always false). The parked count is how the crash fix proves itself live.
	parked := trustResult.Success() &&
		oversized(details, exclusion, p.cfg.MaxAdditions, p.cfg.MaxChangedFiles)
	if parked {
		p.metrics.IncPRPublished("parked")
	} else {
		p.metrics.IncPRPublished("create")
	}
	glog.V(2).Infof("published CreateTaskCommand pr=%s/%s#%d sha=%s taskID=%s trusted=%t parked=%t",
		pr.Owner, pr.Repo, pr.Number, details.HeadSHA, taskIDStr, trustResult.Success(), parked)
	p.recordPublished(ctx, pr, taskIDStr, details)
	return true
}

// recordPublished writes the retry guard's ledger entry for the review task
// this publisher just sent. Without it the ledger would only ever hold forced
// publishes, so a forced retry after a poll-path publish would find no record
// and duplicate the review that is already running.
func (p *taskPublisher) recordPublished(
	ctx context.Context,
	pr PullRequest,
	taskIDStr string,
	details PRDetails,
) {
	if p.recorder == nil {
		return
	}
	p.recorder.RecordPublish(ctx, RetryGuardPR{
		Owner:   pr.Owner,
		Repo:    pr.Repo,
		Number:  pr.Number,
		HeadSHA: details.HeadSHA,
	}, taskIDStr)
}

// ReviewIgnoreExclusion reads the repo's `.reviewignore` and computes what it
// excludes from a PR's size-gate counts. prURL is used only for log context.
//
// Every failure path returns the zero Exclusion — nothing excluded. That is
// the safe direction: an unreadable `.reviewignore` can then only park a PR
// that would otherwise review, never review a PR that should have parked.
//
// The per-file GitHub call is made only when the repo actually has patterns,
// so a repo that never opted in costs exactly one extra API call per PR (the
// contents lookup, which 404s) rather than two.
func ReviewIgnoreExclusion(
	ctx context.Context,
	ghClient GitHubClient,
	owner, repo string,
	number int,
	prURL string,
) Exclusion {
	if ghClient == nil {
		return Exclusion{}
	}
	matcher, err := ghClient.GetReviewIgnore(ctx, owner, repo)
	if err != nil {
		glog.Errorf("get %s failed pr=%s err=%v", reviewignore.Filename, prURL, err)
		return Exclusion{}
	}
	if matcher == nil || matcher.Empty() {
		return Exclusion{}
	}
	files, err := ghClient.ListPRFiles(ctx, owner, repo, number)
	if err != nil {
		glog.Errorf(
			"list pr files for %s failed pr=%s err=%v",
			reviewignore.Filename,
			prURL,
			err,
		)
		return Exclusion{}
	}
	exclusion := ComputeExclusion(files, matcher)
	glog.V(3).Infof(
		"%s excluded additions=%d files=%d pr=%s/%s#%d",
		reviewignore.Filename,
		exclusion.Additions,
		exclusion.Files,
		owner,
		repo,
		number,
	)
	return exclusion
}

// PublishOverride implements TaskPublisher. It emits a `pr-override` task only
// for trusted authors; an untrusted author is skipped (no task, no
// human_review routing) because untrusted PRs are never auto-reviewed and thus
// have no bot verdict to override.
func (p *taskPublisher) PublishOverride(
	ctx context.Context,
	pr PullRequest,
	taskIDStr string,
	details PRDetails,
) (handled, emitted bool) {
	trustResult, err := p.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		// Trust-check error: fall through to the review path (which hits the
		// same error and emits nothing), rather than silently swallowing the PR.
		glog.Errorf("override trust check failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return false, false
	}
	if !trustResult.Success() {
		glog.V(2).Infof("override skipped, untrusted author pr=%s", pr.HTMLURL)
		p.metrics.IncPRPublished("override_skipped")
		return false, false
	}

	cmd := BuildOverrideCommand(
		pr,
		details,
		taskIDStr,
		p.cfg.Stage,
		p.cfg.MaxSlugLen,
		p.cfg.MaxTitleLen,
		p.cfg.TaskSuffix,
		p.cfg.TargetVault,
	)

	if err := p.createSender.SendCommand(ctx, cmd); err != nil {
		// Trusted author: this IS an override case (handled), but the send
		// failed so nothing was emitted. Returning handled=true keeps the
		// caller from emitting a review task for this SHA; emitted=false leaves
		// the override untracked so it retries on the next poll.
		glog.Errorf("publish override task failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return true, false
	}
	glog.V(2).Infof("published override CreateTaskCommand pr=%s/%s#%d sha=%s taskID=%s",
		pr.Owner, pr.Repo, pr.Number, details.HeadSHA, taskIDStr)
	p.metrics.IncPRPublished("override")
	return true, true
}

// PublishMechanicalOverride implements TaskPublisher. The watcher's mechanical
// gate has already evaluated every qualifying condition, so this method only
// builds and sends the command — there is no second trust check.
func (p *taskPublisher) PublishMechanicalOverride(
	ctx context.Context,
	pr PullRequest,
	taskIDStr string,
	details PRDetails,
) bool {
	cmd := BuildMechanicalOverrideCommand(
		pr,
		details,
		taskIDStr,
		p.cfg.Stage,
		p.cfg.MaxSlugLen,
		p.cfg.MaxTitleLen,
		p.cfg.TaskSuffix,
		p.cfg.TargetVault,
	)

	if err := p.createSender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf("publish mechanical override task failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return false
	}
	// Deliberately not V-gated: the spec's post-deploy check greps for this
	// line, so it must be emitted at the default log level.
	glog.Infof("mechanical override pr=%s/%s#%d sha=%s rule=%s",
		pr.Owner, pr.Repo, pr.Number, details.HeadSHA, overrideReasonTrivialBotBump)
	p.metrics.IncPRPublished("override_mechanical")
	return true
}

// NewWatcher returns a Watcher that polls GitHub and publishes commands.
func NewWatcher(
	ghClient GitHubClient,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	startTime libtime.DateTime,
	scope string,
	taskCreationFilter filter.TaskCreationFilter,
	sideEffectFilter filter.TaskCreationFilter,
	overrideLabel string,
	autoMergeLabel string,
	trivialAutoMergeEnabled bool,
	mechanicalOverrideAuthors []string,
	trustDecision trust.Trust,
	unreviewedScanner UnreviewedScanner,
	coalescer coalesce.Coalescer,
	currentDateTime libtime.CurrentDateTimeGetter,
) Watcher {
	return &watcher{
		ghClient:                  ghClient,
		publisher:                 publisher,
		metrics:                   metrics,
		cursorPath:                cursorPath,
		startTime:                 startTime,
		scope:                     scope,
		taskCreationFilter:        taskCreationFilter,
		sideEffectFilter:          sideEffectFilter,
		overrideLabel:             overrideLabel,
		autoMergeLabel:            autoMergeLabel,
		trivialAutoMergeEnabled:   trivialAutoMergeEnabled,
		mechanicalOverrideAuthors: mechanicalOverrideAuthors,
		trustDecision:             trustDecision,
		unreviewedScanner:         unreviewedScanner,
		coalescer:                 coalescer,
		currentDateTime:           currentDateTime,
	}
}

type watcher struct {
	ghClient           GitHubClient
	publisher          TaskPublisher
	metrics            Metrics
	cursorPath         string
	startTime          libtime.DateTime
	scope              string
	taskCreationFilter filter.TaskCreationFilter
	// sideEffectFilter bounds the labeled side-effect pass (processLabeledPRs).
	// Deliberately narrower than taskCreationFilter: the draft and repo-allowlist
	// gates still apply — a dev watcher must not act on repos outside its
	// allowlist, whatever labels they carry — but the age, bot-author and
	// WIP-title gates do not, because those exist to bound review-task creation
	// and the whole point of the pass is to reach PRs the cursor window aged out.
	sideEffectFilter filter.TaskCreationFilter
	// overrideLabel is the PR label that triggers an override task. Empty
	// disables the override path entirely.
	overrideLabel string
	// autoMergeLabel is the PR label that opts the PR into GitHub-native
	// auto-merge. When present on a trusted author's PR, the watcher arms
	// auto-merge (EnableAutoMerge) so GitHub merges once checks + required
	// reviews are green. Empty disables the auto-merge path entirely.
	autoMergeLabel string
	// trivialAutoMergeEnabled turns on the automatic trivial-PR flow: when a
	// repo opts in via `.maintainer.yaml` `autoMerge.trivial` and the PR is
	// mechanically trivial (stage-1 allow-list; the LLM stage-2 fallback is a
	// follow-up), the watcher applies the auto-merge label itself instead of
	// waiting for the author to. Off by default; the flag is the fleet-wide
	// kill switch for the LABELLING path in addition to the per-repo
	// `.maintainer.yaml` gate. It does not govern the mechanical override:
	// that path labels and arms itself, and its only switch is an empty
	// MECHANICAL_OVERRIDE_AUTHORS list — an operator stopping mechanical
	// approvals in an incident must empty that list, not clear this flag.
	trivialAutoMergeEnabled bool
	// mechanicalOverrideAuthors is the exact, case-sensitive set of GitHub
	// logins that may be approved without an LLM review when the repo has
	// opted in. It is NOT BOT_ALLOWLIST (which means "skip entirely") and NOT
	// the trusted-authors list (which means "may be reviewed and may be
	// armed"). Empty means nobody qualifies — the off switch, with no
	// separate enable flag.
	mechanicalOverrideAuthors []string
	// trustDecision gates auto-merge arming to trusted authors (the label
	// alone is insufficient — an untrusted author must not be able to opt
	// any PR into auto-merge).
	trustDecision trust.Trust
	// unreviewedScanner reports open PRs still waiting for a reviewer-bot
	// review. It runs once per poll, after the cursor is saved, and never
	// fails the poll — see UnreviewedScanner.
	unreviewedScanner UnreviewedScanner
	// coalescer bounds how many review records a burst of pushes to one PR can
	// create. It is the same instance the webhook receiver consults, so the two
	// emit paths cannot each admit a record for the same burst.
	coalescer coalesce.Coalescer
	// currentDateTime is the injected clock the coalescer's hold window is
	// measured against.
	currentDateTime libtime.CurrentDateTimeGetter
}

// rateLimitRemainingForGauge returns the value to publish to the gauge at the
// end of a poll. Prefers the transport-captured remaining (updated by any
// core-API call during the cycle); when it is still 0 — a search-only cycle
// with no open PRs makes no core calls, so the gauge would otherwise sit at
// the "unpopulated" sentinel and falsely read as quota exhaustion — falls back
// to the GET /rate_limit core reading (one lightweight core call).
func (w *watcher) rateLimitRemainingForGauge(ctx context.Context) int {
	if remaining := w.ghClient.RateLimitRemaining(); remaining > 0 {
		return remaining
	}
	remaining, err := w.ghClient.GetRateLimitCoreRemaining(ctx)
	if err != nil {
		glog.V(2).Infof("rate-limit probe failed, gauge stays 0: %v", err)
		return 0
	}
	return remaining
}

func (w *watcher) Poll(ctx context.Context) error {
	cursorState, err := LoadCursor(ctx, w.cursorPath, w.startTime)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor")
	}
	// Publish the shared-token remaining quota on every exit path (success,
	// rate-limited abort, github-error abort) so the rate_limit_remaining
	// gauge tracks the last API response's X-RateLimit-Remaining — the alert
	// surface for quota exhaustion before the fleet-wide 403 stall.
	defer w.metrics.SetRateLimitRemaining(w.rateLimitRemainingForGauge(ctx))

	allPRs, abortReason := w.fetchAllPRs(ctx, cursorState.LastUpdatedAt)
	if abortReason != "" {
		w.metrics.IncPollCycle(abortReason)
		return nil
	}

	select {
	case <-ctx.Done():
		return nil
	default:
	}

	maxUpdatedAt, sideEffectsRun := w.processPRs(ctx, &cursorState, allPRs)

	// Side-effect pass, deliberately outside the cursor window above. That
	// window is `updated:>=cursor` and the cursor only ever advances, so a PR
	// that has stopped being updated — a stale branch, which is exactly the
	// population these side effects exist for — is never fetched by the main
	// loop again. Re-query by label so a labeled PR stays reachable however
	// old it is. PRs already handled above are skipped, not re-run.
	w.processLabeledPRs(ctx, sideEffectsRun)

	if maxUpdatedAt.After(cursorState.LastUpdatedAt) {
		cursorState.LastUpdatedAt = maxUpdatedAt
	}

	if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil {
		glog.Errorf("failed to save cursor err=%v", err)
	}
	// Unreviewed-PR scan runs after the cursor is persisted so a slow scan
	// never delays it, and before the cycle is counted successful so the
	// scan's outcome never changes the poll result. A scan skipped here (a
	// main-search abort returns earlier) is intended: the previous gauge
	// values stay in place.
	w.unreviewedScanner.Scan(ctx)
	w.metrics.IncPollCycle("success")
	return nil
}

// fetchAllPRs paginates GitHub search results. Returns (prs, "") on success,
// or (nil, reason) where reason is "github_error" or "rate_limited" if the caller should abort.
func (w *watcher) fetchAllPRs(
	ctx context.Context,
	since libtime.DateTime,
) ([]PullRequest, string) {
	page := 1
	var allPRs []PullRequest

	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("fetchAllPRs cancelled before page search")
			return nil, ""
		default:
		}

		result, err := w.ghClient.SearchPRs(ctx, w.scope, since, page)
		if err != nil {
			glog.Errorf("github search failed err=%v", err)
			return nil, "github_error"
		}

		allPRs = append(allPRs, result.PullRequests...)

		if !result.HasNextPage {
			break
		}
		page = result.NextPage
	}
	return allPRs, ""
}

// processPRs iterates over fetched PRs, publishes commands, and returns the max updated-at seen
// plus the set of PRs whose side effects (update-branch, auto-merge, supersede) already ran this
// poll — processLabeledPRs skips those so no side effect executes twice in one cycle.
// It rebuilds HeadSHAs from only the current open-PR batch, pruning closed/merged PRs.
// Each (PR, SHA) pair produces at most one CreateTaskCommand across all poll cycles.
//
// Design note on cursor preservation for filter-skipped and details-fetch-error PRs:
//
// CRITICAL ASSUMPTION: the controller deduplicates incoming CreateTaskCommands by their
// task_identifier (UUID5). If the controller does NOT dedup, every transient filter toggle
// or transient GetPRDetails failure will produce a duplicate vault file on the next poll.
// VERIFY this assumption against the controller code before merging — search for the
// command consumer's idempotency check; if absent, this design must change to preserve
// per-PR cursor entries (which would require extending the cursor schema with the
// (owner, repo, number) tuple, since UUID5 is not reversible).
//
// Given the assumption holds, we accept that transient filter or fetch failures will cause
// the watcher to re-publish a CreateTaskCommand for the same (PR, SHA) on the next successful
// poll, and rely on controller dedup to make this a no-op. This matches the recovery path
// already documented in the spec failure-mode row "Watcher restart with empty cursor sees a
// PR whose head SHA already has a vault file" — same mechanism, slightly different trigger.
func (w *watcher) processPRs(
	ctx context.Context,
	cursorState *Cursor,
	allPRs []PullRequest,
) (libtime.DateTime, map[string]struct{}) {
	maxUpdatedAt := cursorState.LastUpdatedAt
	prDetailsCache := make(map[string]PRDetails)
	newHeadSHAs := make(map[string]string, len(allPRs))
	sideEffectsRun := make(map[string]struct{}, len(allPRs))
	var deferFloor libtime.DateTime
	deferFloorSet := false

	for _, pr := range allPRs {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("poll cancelled during processPRs at pr %d", pr.Number)
			return maxUpdatedAt, sideEffectsRun
		default:
		}

		outcome := w.processPR(
			ctx, pr, cursorState, newHeadSHAs, prDetailsCache, sideEffectsRun,
		)
		if outcome.deferred {
			if !deferFloorSet || pr.UpdatedAt.Before(deferFloor) {
				deferFloor = pr.UpdatedAt
				deferFloorSet = true
			}
		}
		if outcome.handled && outcome.updatedAt.After(maxUpdatedAt) {
			maxUpdatedAt = outcome.updatedAt
		}
	}

	// A deferred PR is owed another attempt, and a deferral writes no cursor
	// entry, so the search window is the only thing that can bring it back. That
	// window is `updated:>=cursor` and inclusive, so clamping the cursor to the
	// earliest deferred PR keeps it in range. Without the clamp any other PR
	// updated later in this same batch would push the cursor past it and the PR
	// would never be fetched again — no review and no override, permanently,
	// because the unreviewed scanner that would otherwise notice is off unless
	// REVIEWER_BOT_LOGINS is configured.
	if deferFloorSet && maxUpdatedAt.After(deferFloor) {
		maxUpdatedAt = deferFloor
	}

	cursorState.HeadSHAs = newHeadSHAs
	return maxUpdatedAt, sideEffectsRun
}

// prKey identifies a PR for the cursor and side-effect bookkeeping.
func prKey(pr PullRequest) string {
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
}

// processLabeledPRs runs the side-effect half of processPR over every open PR
// carrying the auto-merge label, with no cursor bound.
//
// Why a separate pass: the main loop only sees PRs the search returns, and
// that search is `updated:>=cursor` against a cursor that only ever advances
// (see Poll). A branch goes stale precisely by not being touched, so its
// updated-at falls behind the cursor and the PR leaves the window for good —
// which makes update-branch, auto-merge and supersede unreachable for exactly
// the population they exist to serve. Querying by label has no such blind
// spot: a labeled PR stays reachable for as long as the label is present.
//
// sideEffectFilter — not taskCreationFilter — bounds this pass. The repo
// allowlist and draft gates still apply, because a dev watcher must not act on
// repos outside its allowlist however they are labeled; the age, bot-author and
// WIP-title gates do not, since those bound review-task creation and reaching
// cursor-aged PRs is the entire point. The trust gate inside each try* call
// remains the boundary that decides whether anything actually happens.
//
// skip holds PRs whose side effects already ran in the main loop this poll;
// they are not repeated. Failures never abort the poll.
func (w *watcher) processLabeledPRs(
	ctx context.Context,
	skip map[string]struct{},
) {
	if w.autoMergeLabel == "" {
		return
	}
	prs, reason := w.fetchLabeledPRs(ctx)
	if reason != "" {
		glog.Warningf("labeled pr fetch failed reason=%s", reason)
		return
	}
	prDetailsCache := make(map[string]PRDetails)
	for _, pr := range prs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, ok := skip[prKey(pr)]; ok {
			continue
		}
		if w.sideEffectFilter != nil && w.sideEffectFilter.Skip(filter.PR{
			AuthorLogin: pr.AuthorLogin,
			IsDraft:     pr.IsDraft,
			Title:       pr.Title,
			UpdatedAt:   pr.UpdatedAt,
			RepoKey:     "github.com/" + pr.Owner + "/" + pr.Repo,
		}) {
			glog.V(3).Infof("labeled pass skipping pr=%s/%s#%d reason=filtered",
				pr.Owner, pr.Repo, pr.Number)
			continue
		}
		details, err := w.fetchPRDetails(ctx, pr, prDetailsCache)
		if err != nil {
			glog.Errorf(
				"get pr details failed pr=%s/%s#%d err=%v",
				pr.Owner,
				pr.Repo,
				pr.Number,
				err,
			)
			continue
		}
		if details.HeadSHA == "" {
			glog.Warningf(
				"missing head SHA for pr=%s/%s#%d, skipping",
				pr.Owner,
				pr.Repo,
				pr.Number,
			)
			continue
		}
		w.trySupersedeDepBump(ctx, pr, details)
		w.tryUpdateBranch(ctx, pr, details)
		w.tryAutoMerge(ctx, pr, details)
	}
}

// fetchLabeledPRs paginates the labeled-PR search. Returns (prs, "") on
// success, or (nil, reason) mirroring fetchAllPRs.
func (w *watcher) fetchLabeledPRs(ctx context.Context) ([]PullRequest, string) {
	page := 1
	var allPRs []PullRequest

	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("fetchLabeledPRs cancelled before page search")
			return nil, ""
		default:
		}

		result, err := w.ghClient.SearchLabeledPRs(ctx, w.scope, w.autoMergeLabel, page)
		if err != nil {
			glog.Errorf("github labeled search failed err=%v", err)
			return nil, "github_error"
		}

		allPRs = append(allPRs, result.PullRequests...)

		if !result.HasNextPage {
			break
		}
		page = result.NextPage
	}
	return allPRs, ""
}

// processPR processes a single PR: filter → fetch details → override-or-review.
// It returns handled when the PR was settled (override emitted, review deduped,
// or review published), and deferred when the mechanical-override gate could not
// be evaluated — nothing published, no cursor entry written, and the caller must
// keep the cursor from advancing past it. Anything else is unhandled and must
// not advance the cursor for its own sake. cursorState and newHeadSHAs are
// mutated in place.
func (w *watcher) processPR(
	ctx context.Context,
	pr PullRequest,
	cursorState *Cursor,
	newHeadSHAs map[string]string,
	prDetailsCache map[string]PRDetails,
	sideEffectsRun map[string]struct{},
) prOutcome {
	if w.taskCreationFilter.Skip(
		filter.PR{
			AuthorLogin: pr.AuthorLogin,
			IsDraft:     pr.IsDraft,
			Title:       pr.Title,
			UpdatedAt:   pr.UpdatedAt,
			RepoKey:     "github.com/" + pr.Owner + "/" + pr.Repo,
		},
	) {
		glog.V(3).Infof("skipping pr=%s/%s#%d reason=filtered", pr.Owner, pr.Repo, pr.Number)
		w.metrics.IncPRPublished("skipped")
		// Filtered PRs do not contribute entries to newHeadSHAs. If the PR was previously
		// published, its SHA-based cursor entry is pruned here and will be re-created on
		// the next successful (non-filtered) poll — controller dedup prevents a duplicate file.
		return prOutcome{}
	}

	details, err := w.fetchPRDetails(ctx, pr, prDetailsCache)
	if err != nil {
		glog.Errorf("get pr details failed pr=%s/%s#%d err=%v", pr.Owner, pr.Repo, pr.Number, err)
		// Same rationale as filtered PRs: cannot preserve old SHA-based entry without
		// knowing the SHA. Transient error → re-publish on next poll → controller deduplicates.
		return prOutcome{}
	}

	// Fail-closed: if head SHA is absent, skip this PR on this poll.
	if details.HeadSHA == "" {
		glog.Warningf("missing head SHA for pr=%s/%s#%d, skipping", pr.Owner, pr.Repo, pr.Number)
		return prOutcome{}
	}

	// Automatic trivial flow: when the repo opted in via `.maintainer.yaml`
	// `autoMerge.trivial` and the PR is mechanically trivial, apply the
	// auto-merge label so the arming path below picks it up this poll. Never
	// merges directly — GitHub-native auto-merge on green executes the merge
	// (never-merge boundary held). The author-applied label is not needed.
	if w.trivialAutoMergeEnabled && !slices.Contains(pr.Labels, w.autoMergeLabel) &&
		w.maybeLabelTrivial(ctx, pr) {
		pr.Labels = append(pr.Labels, w.autoMergeLabel)
	}

	// The side effects below are recorded so processLabeledPRs — which runs
	// outside the cursor window and covers a superset of these PRs — does not
	// repeat them in the same poll.
	sideEffectsRun[prKey(pr)] = struct{}{}

	// Supersession: a dep-bump PR that has gone dirty is retired rather than
	// refreshed — see trySupersedeDepBump for why merging master in cannot
	// resolve it. Runs before the branch-freshness step so the abort path owns
	// the dirty-dep-bump case outright; tryUpdateBranch skips that same case.
	w.trySupersedeDepBump(ctx, pr, details)

	// Branch freshness: a labeled, trusted PR whose head branch has gone stale
	// against its base cannot be merged by GitHub-native auto-merge however
	// green it is — master moving strands it. Refresh the branch first, then
	// (re-)arm below; arming is idempotent, so ordering update-branch ahead of
	// it lets a single poll both refresh the branch and re-arm. Side effect
	// only; the review path continues unchanged.
	w.tryUpdateBranch(ctx, pr, details)

	// Auto-merge arming: a trusted author carrying the auto-merge label opts
	// the PR into GitHub-native auto-merge. Side effect only — the review path
	// continues unchanged below (the ruleset's required review must still
	// approve before GitHub merges).
	w.tryAutoMerge(ctx, pr, details)

	// Override path: a trusted author carrying the override label gets a
	// `pr-override` task INSTEAD of a review task for this SHA. Emitting both
	// would race — the review could post CHANGES_REQUESTED after the override's
	// APPROVE and re-block merge. tryOverride returns false (no label, untrusted
	// author, or send error) → fall through to the normal review path.
	if w.tryOverride(ctx, pr, details, cursorState.HeadSHAs, newHeadSHAs) {
		return prOutcome{updatedAt: pr.UpdatedAt, handled: true}
	}

	// Mechanical override: a qualifying trivial bot bump gets an approve instead
	// of a review for this SHA. Emitting both would race — a review could post
	// CHANGES_REQUESTED after the override's APPROVE and re-block merge.
	if outcome := w.mechanicalOverrideOutcome(
		ctx, pr, details, cursorState, newHeadSHAs,
	); outcome.handled {
		return outcome
	}

	taskIDStr := DeriveTaskID(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()

	if _, exists := cursorState.HeadSHAs[taskIDStr]; exists {
		// Same (PR, SHA) already spawned — no-op.
		glog.V(3).Infof(
			"no change, skipping pr=%s/%s#%d sha=%s taskID=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA, taskIDStr,
		)
		newHeadSHAs[taskIDStr] = details.HeadSHA
		return prOutcome{updatedAt: pr.UpdatedAt, handled: true}
	}

	// New (PR, SHA) pair — publish a fresh CreateTaskCommand.
	if !w.admitReview(pr, details) {
		// Deferred, not handled: nothing was published and no cursor entry was
		// written, so the cursor must be clamped to this PR or the next search
		// window would never return it and the trailing review would be lost.
		return prOutcome{deferred: true}
	}
	if w.publisher.PublishCreate(ctx, pr, taskIDStr, details) {
		// Update cursorState in-place so duplicate PR entries in the same poll batch
		// are deduplicated without a second create publish.
		cursorState.HeadSHAs[taskIDStr] = details.HeadSHA
		newHeadSHAs[taskIDStr] = details.HeadSHA
		return prOutcome{updatedAt: pr.UpdatedAt, handled: true}
	}

	return prOutcome{}
}

// admitReview reports whether the coalescer allows a review record for this
// head. Trailing coalesce: a push that lands while the PR is still moving is
// held for the trailing dispatch instead of becoming its own record. The same
// coalescer instance gates the webhook path, so a burst cannot gain a third
// record here after the webhook already emitted one.
func (w *watcher) admitReview(pr PullRequest, details PRDetails) bool {
	key := coalesce.Key{Owner: pr.Owner, Repo: pr.Repo, Number: pr.Number}
	if w.coalescer.Admit(
		key,
		details.HeadSHA,
		details.UpdatedAt,
		w.currentDateTime.Now(),
	) {
		return true
	}
	glog.V(2).Infof(
		"coalesced pr=%s/%s#%d sha=%s", pr.Owner, pr.Repo, pr.Number, details.HeadSHA,
	)
	w.metrics.IncPRPublished("coalesced")
	return false
}

// mechanicalOverrideOutcome runs the mechanical-override attempt and owns the
// deferral bookkeeping that goes with it.
//
// A deferral is the one outcome that must neither review nor advance the cursor:
// nothing was published, so the PR is still owed a decision, and the only thing
// that brings it back is the next search window — which is why processPRs holds
// the cursor at a deferred PR. The count is bounded so a gate that never becomes
// readable, a revoked token or a changed permission, cannot pin that window
// forever. On exhaustion the PR takes the review the pre-change code always gave
// it: a reviewer pod spent, but far better than a PR nobody looks at again.
func (w *watcher) mechanicalOverrideOutcome(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
	cursorState *Cursor,
	newHeadSHAs map[string]string,
) prOutcome {
	key := deferralKey(pr, details.HeadSHA)
	switch w.tryMechanicalOverride(ctx, pr, details, cursorState.HeadSHAs, newHeadSHAs) {
	case overrideHandled:
		delete(cursorState.Deferrals, key)
		return prOutcome{updatedAt: pr.UpdatedAt, handled: true}
	case overrideDeferred:
		cursorState.Deferrals[key]++
		if cursorState.Deferrals[key] <= maxDeferralAttempts {
			glog.Infof(
				"mechanical override: gate could not be evaluated, deferring pr=%s/%s#%d attempt=%d",
				pr.Owner,
				pr.Repo,
				pr.Number,
				cursorState.Deferrals[key],
			)
			return prOutcome{updatedAt: pr.UpdatedAt, handled: true, deferred: true}
		}
		glog.Warningf(
			"mechanical override: gate still unreadable after %d attempts, falling through to review pr=%s/%s#%d",
			maxDeferralAttempts,
			pr.Owner,
			pr.Repo,
			pr.Number,
		)
	}
	delete(cursorState.Deferrals, key)
	return prOutcome{}
}

// tryOverride handles the override path for one PR. It returns true when the PR
// was handled as an override (the caller then skips the review path for this
// SHA). It returns false — leaving the caller to fall through to the normal
// review path — when there is no override label, the label is absent, the
// author is untrusted, or publishing failed. On a successful (or already-seen)
// override it records the override task-id in both cursor maps so the override
// fires at most once per head SHA. cursorHeadSHAs and newHeadSHAs are maps
// (reference types); mutating them here updates the caller's cursor.
func (w *watcher) tryOverride(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
	cursorHeadSHAs map[string]string,
	newHeadSHAs map[string]string,
) bool {
	if w.overrideLabel == "" || !slices.Contains(pr.Labels, w.overrideLabel) {
		return false
	}
	overrideID := DeriveTaskIDOverride(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()
	if _, exists := cursorHeadSHAs[overrideID]; exists {
		glog.V(3).Infof(
			"override already emitted, skipping pr=%s/%s#%d sha=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA,
		)
		newHeadSHAs[overrideID] = details.HeadSHA
		return true
	}
	handled, emitted := w.publisher.PublishOverride(ctx, pr, overrideID, details)
	if emitted {
		// Record the override task-id only when it was actually sent, so a
		// send failure retries next poll instead of being deduped away.
		cursorHeadSHAs[overrideID] = details.HeadSHA
		newHeadSHAs[overrideID] = details.HeadSHA
	}
	return handled
}

// tryMechanicalOverride emits the mechanical override task for a qualifying
// trivial bot bump. It returns true when the PR is handled (override emitted,
// already emitted for this SHA, or the send failed) — the caller then skips the
// review path for this SHA, preserving the override-only invariant that avoids
// the APPROVE-vs-CHANGES_REQUESTED race. It returns overrideNotHandled when the
// PR does not qualify, so the caller falls through to the normal review.
//
// The override task-id is the same one tryOverride uses, so both override kinds
// share one dedupe entry per (PR, head SHA). A failed send records nothing, so
// the emission retries on the next poll.
//
// A qualifying PR is labelled with the auto-merge label and armed for
// GitHub-native auto-merge in the same poll that publishes the approve. A
// failed label write returns overrideNotHandled so the caller reviews the PR
// this poll instead — an approve never lands without its arming. A failed arming
// after a successful label write does not change the return value: the label is
// present, so the next poll's tryAutoMerge retries the arming.
func (w *watcher) tryMechanicalOverride(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
	cursorHeadSHAs map[string]string,
	newHeadSHAs map[string]string,
) overrideOutcome {
	overrideID := DeriveTaskIDOverride(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()
	if _, exists := cursorHeadSHAs[overrideID]; exists {
		glog.V(3).Infof(
			"mechanical override already emitted, skipping pr=%s/%s#%d sha=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA,
		)
		newHeadSHAs[overrideID] = details.HeadSHA
		return overrideHandled
	}
	// Dedupe is checked before the gate: the gate costs two uncached GitHub
	// reads, and a SHA that was already handled must not pay for them on every
	// poll. The only ordering the design needs is label-before-publish.
	//
	// A SHA that already carries a review task is never overridden afterwards:
	// a review and an override for the same commit race, and the review could
	// post CHANGES_REQUESTED after the approve. This is reachable through a
	// transient label-write failure on an earlier poll.
	if _, exists := cursorHeadSHAs[DeriveTaskID(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()]; exists {
		glog.V(3).Infof(
			"mechanical override skipped, review already emitted pr=%s/%s#%d sha=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA,
		)
		return overrideNotHandled
	}
	// A gate that could not be evaluated is NOT a rejection. Returning deferred
	// means "emit nothing for this PR this poll" — the caller skips the review
	// path AND holds the cursor back so the PR stays in the next search window.
	// Falling through to a review instead would write a review-task cursor entry
	// and permanently block the override for this SHA (see the
	// review-already-emitted guard above), so a single transient read failure
	// would cost a full LLM review forever.
	switch w.qualifiesForMechanicalOverride(ctx, pr, details) {
	case overrideIndeterminate:
		return overrideDeferred
	case overrideDoesNotQualify:
		return overrideNotHandled
	}
	// Label and arm before publishing the approve: an approve that can never
	// merge because nothing armed the PR is the stranded-approve failure this
	// feature exists to remove. The gate guarantees a non-empty auto-merge
	// label, so no second emptiness check is needed.
	//
	// A failed label write returns false so the caller falls through to a
	// normal review this poll — an approve must never land without its arming.
	// That fall-through cannot later become an override of the same SHA: the
	// review-task cursor entry makes tryMechanicalOverride skip it.
	// A failed arming after a successful label write is tolerated: the label is
	// now present, so the next poll's tryAutoMerge path retries and succeeds.
	// When the label is already present, tryAutoMerge already ran earlier in
	// this poll and armed the PR; re-arming is skipped here for the same reason.
	if !slices.Contains(pr.Labels, w.autoMergeLabel) {
		if err := w.ghClient.AddLabel(
			ctx, pr.Owner, pr.Repo, pr.Number, w.autoMergeLabel,
		); err != nil {
			glog.Errorf("mechanical override: add label failed pr=%s err=%v", pr.HTMLURL, err)
			w.metrics.IncPRPublished("error")
			return overrideNotHandled
		}
		// Arm through the one arming path, so the trust check, the log line and
		// the auto_merge counter stay in a single place. The label is present by
		// now, which is exactly what tryAutoMerge requires. A failure here is
		// tolerated: the label persists, so the next poll retries the arming.
		pr.Labels = append(pr.Labels, w.autoMergeLabel)
		w.tryAutoMerge(ctx, pr, details)
	}
	if w.publisher.PublishMechanicalOverride(ctx, pr, overrideID, details) {
		cursorHeadSHAs[overrideID] = details.HeadSHA
		newHeadSHAs[overrideID] = details.HeadSHA
	}
	// Handled whether or not the send succeeded: a failed send must not fall
	// through to a review for this SHA, and the unset cursor retries next poll.
	return overrideHandled
}

// MergeState is a GitHub REST merge-state value, typed so a state-name typo is
// caught by the compiler rather than silently never matching.
//
// The set below is deliberately partial: it names only the states the watcher
// reasons about, not GitHub's full vocabulary (`clean`, `blocked`, `unstable`,
// and `unknown` are all real values that simply never drive a decision here).
// An unrecognized value is never stale, which is the safe default.
type MergeState string

const (
	// MergeStateBehind is a head branch behind its base with no conflict —
	// the case update-branch resolves.
	MergeStateBehind MergeState = "behind"
	// MergeStateDirty is a PR whose merge commit cannot be created (a real
	// conflict). update-branch does not resolve it.
	MergeStateDirty MergeState = "dirty"
)

// AvailableMergeStates lists every merge state the watcher acts on.
var AvailableMergeStates = []MergeState{MergeStateBehind, MergeStateDirty}

// staleMergeState reports whether a merge state means the head branch is out
// of date against its base.
//
// Behind is the mechanical case update-branch resolves. Dirty is included
// because GitHub computes mergeability lazily and caches it, so a dirty
// reading can be stale; the attempt is cheap and a real conflict simply fails.
// Unknown is deliberately excluded — it means GitHub has not computed the
// state yet, so acting on it would fire an update-branch call on every poll
// until the state settles.
func staleMergeState(state MergeState) bool {
	return state == MergeStateBehind || state == MergeStateDirty
}

// tryUpdateBranch merges the base branch into the head branch of a labeled PR
// from a trusted author whose branch has gone stale against its base. Side
// effect only — the caller continues regardless.
//
// This closes the residue the arming path cannot: a PR that was APPROVED and
// green when opened turns `behind` (or `dirty`) as soon as master moves, and
// GitHub-native auto-merge cannot fire until the branch is current again. The
// watcher performs only the mechanical half — GitHub still executes the merge,
// holding the same never-merge boundary as tryAutoMerge.
//
// Gated on the same population as tryAutoMerge: the auto-merge label plus a
// trusted author. A PR already opted into auto-merge is exactly the PR whose
// staleness strands the loop, so no separate per-repo opt-in is required.
//
// `dirty` means a real conflict, which update-branch cannot resolve — it
// merges base into head, so a conflicting merge fails. A failure is logged and
// the caller continues normally; the conflicting-dep-bump case belongs to the
// update-go abort-and-reemit policy, not here.
//
// Failures never block the review path: like tryAutoMerge this is a side
// effect, and the caller continues regardless.
func (w *watcher) tryUpdateBranch(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) {
	if w.autoMergeLabel == "" || !slices.Contains(pr.Labels, w.autoMergeLabel) {
		return
	}
	// A dirty dep-bump PR is the abort-and-reemit case, not a refresh case —
	// merging master in cannot resolve it (see isSupersededDepBump). Let the
	// abort path own that PR outright rather than attempting an update that
	// will 422.
	if isSupersededDepBump(pr, details) {
		return
	}
	if !staleMergeState(details.MergeableState) {
		return
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		glog.Errorf("update-branch trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return
	}
	if !trustResult.Success() {
		glog.V(2).Infof("update-branch skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("update_branch_skipped")
		return
	}
	if err := w.ghClient.UpdateBranch(ctx, pr.Owner, pr.Repo, pr.Number); err != nil {
		// Expected for a genuinely conflicting (`dirty`) PR — update-branch
		// resolves staleness, not conflicts. Logged, not fatal.
		glog.V(2).Infof("update branch failed pr=%s/%s#%d state=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, details.MergeableState, err)
		w.metrics.IncPRPublished("update_branch_failed")
		return
	}
	glog.V(2).Infof("updated branch pr=%s/%s#%d state=%s",
		pr.Owner, pr.Repo, pr.Number, details.MergeableState)
	w.metrics.IncPRPublished("update_branch")
}

// updateGoBranchPrefix is the head-branch prefix the update-go agent gives its
// dep-bump PRs (`fix/update-go-<sha>`). Verified across five live PRs
// (go-skeleton #107/#104/#103, alert-controller #11, notification #1); the
// agent's non-dep-bump work uses ordinary prefixes like `feature/…`.
const updateGoBranchPrefix = "fix/update-go-"

// botLoginSuffix is how GitHub renders an App's login in the API
// (`ben-s-go-updater-dev[bot]`), as opposed to the `app/<slug>` form the web
// UI shows.
const botLoginSuffix = "[bot]"

// depBumpSupersededComment explains the close on the PR itself, so the
// supersession is legible to whoever finds it later rather than reading as an
// arbitrary closure.
const depBumpSupersededComment = "Closing as superseded: this dependency-bump PR conflicts with a release that " +
	"renamed `## Unreleased` in CHANGELOG.md. That conflict is structural — every bump edits the same region — " +
	"so the branch cannot be refreshed by merging master in.\n\n" +
	"A fresh bump off current master replaces it. The update-go watcher emits one task per repo HEAD ref, and " +
	"master moving is what put this PR in conflict."

// isSupersededDepBump reports whether pr is an update-go dep-bump PR that a
// conflicting merge has stranded — the case the abort-and-reemit policy exists
// for.
//
// Both identity conditions are required:
//   - the head branch carries the update-go prefix, and
//   - the author is an App (`<slug>[bot]`).
//
// The author check is what holds the task's Out-of-Scope boundary: a
// human-authored PR is never auto-closed, however it happens to be named.
//
// `dirty` is required because that is the state abort-and-reemit resolves and
// update-branch cannot — DIRTY means the merge commit cannot be created, so
// merging base into head fails. `behind` belongs to tryUpdateBranch, which is
// strictly cheaper: it keeps the PR and its review.
func isSupersededDepBump(pr PullRequest, details PRDetails) bool {
	if !strings.HasPrefix(details.HeadRef, updateGoBranchPrefix) {
		return false
	}
	if !strings.HasSuffix(pr.AuthorLogin, botLoginSuffix) {
		return false
	}
	return details.MergeableState == MergeStateDirty
}

// trySupersedeDepBump retires a dep-bump PR that a conflicting merge has
// stranded, so the update-go pipeline can replace it with a fresh one off
// current master.
//
// Why close rather than refresh: the CHANGELOG fold race. A release renames
// `## Unreleased`, and every open dep-bump PR edits that same region, so the
// conflict is structural — update-branch merges base into head and a
// conflicting merge fails. Regenerating off current master is the only
// resolution that does not require hand-editing a generated file.
//
// The successor arrives on its own: the update-go watcher emits one task per
// repo HEAD ref, and master having moved is precisely what made this PR dirty,
// so a fresh task (and PR) follows.
//
// Gated on the same trusted-author check as the rest of the watcher — an
// untrusted author's PR is left alone, never closed.
//
// Failures never block the review path: the caller continues regardless.
func (w *watcher) trySupersedeDepBump(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) {
	if !isSupersededDepBump(pr, details) {
		return
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		glog.Errorf("abort-and-reemit trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return
	}
	if !trustResult.Success() {
		glog.V(2).Infof("abort-and-reemit skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("dep_bump_skipped")
		return
	}
	if err := w.ghClient.ClosePR(
		ctx, pr.Owner, pr.Repo, pr.Number, depBumpSupersededComment,
	); err != nil {
		glog.Errorf("close superseded dep-bump pr failed pr=%s/%s#%d err=%v",
			pr.Owner, pr.Repo, pr.Number, err)
		w.metrics.IncPRPublished("error")
		return
	}
	glog.V(2).Infof("closed superseded dep-bump pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	w.metrics.IncPRPublished("dep_bump_superseded")
}

// tryAutoMerge arms GitHub-native auto-merge for a labeled PR from a trusted
// author. It does nothing — leaving the caller to continue normally — when the
// auto-merge path is disabled (empty label), the label is absent, the author
// is untrusted, or arming failed.
//
// Arming is independent of the review path: it does NOT skip review. The PR
// still flows through the normal review (the ruleset requires an approving
// review before GitHub auto-merges), and GitHub's auto-merge fires only when
// both required checks and required reviews are green. Arming is idempotent
// at the GitHub API level, so re-arming an already-armed PR on a later poll
// is a success no-op; EnableAutoMerge errors are logged and the review path
// continues.
func (w *watcher) tryAutoMerge(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) {
	if w.autoMergeLabel == "" || !slices.Contains(pr.Labels, w.autoMergeLabel) {
		return
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		glog.Errorf("auto-merge trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return
	}
	if !trustResult.Success() {
		glog.V(2).Infof("auto-merge skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("auto_merge_skipped")
		return
	}
	if err := w.ghClient.EnableAutoMerge(ctx, pr.Owner, pr.Repo, pr.Number); err != nil {
		glog.Errorf("enable auto-merge failed pr=%s/%s#%d err=%v",
			pr.Owner, pr.Repo, pr.Number, err)
		w.metrics.IncPRPublished("error")
		return
	}
	glog.V(2).Infof("armed auto-merge pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	w.metrics.IncPRPublished("auto_merge")
}

// maybeLabelTrivial applies the auto-merge label to a mechanically trivial PR
// when the repo opted in via `.maintainer.yaml` `autoMerge.trivial`. It returns
// true when the label was applied, false otherwise (not opted in, not trivial,
// or a transient error). This is the automatic counterpart to the author-applied
// label — the watcher decides triviality itself. It never merges directly: the
// label only feeds the arming path (tryAutoMerge), which delegates the merge to
// GitHub-native auto-merge on green. Callers must have already confirmed the
// label is absent.
func (w *watcher) maybeLabelTrivial(ctx context.Context, pr PullRequest) bool {
	cfg, err := w.ghClient.GetMaintainerConfig(ctx, pr.Owner, pr.Repo)
	if err != nil {
		glog.Errorf(
			"trivial auto-merge: get maintainer config failed pr=%s err=%v",
			pr.HTMLURL,
			err,
		)
		w.metrics.IncPRPublished("error")
		return false
	}
	if !cfg.AutoMerge.Trivial {
		glog.V(3).Infof("trivial auto-merge: repo not opted in pr=%s/%s", pr.Owner, pr.Repo)
		return false
	}
	files, err := w.ghClient.ListPRFiles(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		glog.Errorf("trivial auto-merge: list pr files failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	if !ClassifyTrivial(files) {
		glog.V(3).Infof("trivial auto-merge: not trivial pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
		w.metrics.IncPRPublished("auto_merge_not_trivial")
		return false
	}
	if err := w.ghClient.AddLabel(ctx, pr.Owner, pr.Repo, pr.Number, w.autoMergeLabel); err != nil {
		glog.Errorf("trivial auto-merge: add label failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	glog.V(2).Infof("trivial auto-merge: labeled pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	w.metrics.IncPRPublished("auto_merge_labeled")
	return true
}

// overrideQualification is the three-valued answer to "may this PR be approved
// without an LLM review?". It is deliberately not a bool: "no" and "I could not
// tell" demand opposite responses from the caller, and treating them alike is
// what turned a transient read failure into a permanently spent review.
type overrideQualification int

const (
	// overrideIndeterminate means a gate could not be evaluated because a
	// GitHub read failed. The caller must publish nothing for this PR this
	// poll and retry on the next one.
	overrideIndeterminate overrideQualification = iota
	// overrideDoesNotQualify means a gate evaluated and rejected the PR. The
	// caller falls through to the normal review path.
	overrideDoesNotQualify
	// overrideQualifies means every gate passed.
	overrideQualifies
)

// overrideOutcome is what the mechanical-override path decided for one PR. It is
// three-valued for the same reason the qualification is: "handled" skips the
// review, "not handled" runs it, and "deferred" must do neither while also
// holding the cursor back so the PR is fetched again.
type overrideOutcome int

const (
	// overrideNotHandled — no override applies; the caller runs the review path.
	overrideNotHandled overrideOutcome = iota
	// overrideHandled — an override was published, or already existed for this SHA.
	overrideHandled
	// overrideDeferred — the gate could not be evaluated. Nothing was published
	// and no cursor entry was written, so the PR is owed another attempt.
	overrideDeferred
)

// maxDeferralAttempts bounds how many consecutive polls a PR may be deferred
// before it falls through to a review after all. Deferring is only safe because
// the cursor is held back, and holding it back re-fetches everything newer than
// the deferred PR on every poll — so an unreadable gate must not be able to pin
// the window indefinitely. On exhaustion the PR gets the review the pre-change
// code always gave it: worse for one pod than an override, far better than a PR
// that is never looked at again.
const maxDeferralAttempts = 3

// deferralKey identifies one (PR, head SHA) across polls for the deferral count.
// The SHA is part of the key so a push starts a fresh budget.
func deferralKey(pr PullRequest, headSHA string) string {
	return prKey(pr) + "@" + headSHA
}

// prOutcome is what processPR did with one PR, and in particular whether the
// cursor may advance past it.
type prOutcome struct {
	// updatedAt is the PR's updated-at, valid only when handled.
	updatedAt libtime.DateTime
	// handled means the PR was settled this poll: a review or override was
	// published, or already existed for this SHA.
	handled bool
	// deferred means the override gate could not be evaluated: nothing was
	// published and no cursor entry written, so the cursor must not advance past
	// this PR or the next search would never return it again.
	deferred bool
}

// qualifiesForMechanicalOverride reports whether pr is a trivial bot bump the
// operator has explicitly allowed to be approved without an LLM review.
//
// Every condition must hold, evaluated in this order with short-circuiting:
//  1. the auto-merge label is configured — an approve that cannot be armed is
//     the stranded-approve failure this feature exists to remove;
//  2. the author is on the mechanical-override list (exact, case-sensitive);
//  3. the author is trusted;
//  4. the repo opted in via both `.maintainer.yaml` flags
//     (`autoMerge.trivial` and `prReviewer.autoApprove`);
//  5. every changed file is mechanical per ClassifyTrivial.
//
// Conditions 1 and 2 precede every GitHub read, so a PR whose author is not on
// the list costs no extra API call. The function never mutates pr, writes to
// GitHub, or publishes a command.
//
// A gate that REJECTS the PR and a gate that FAILS to evaluate it are different
// answers, which is why this returns overrideQualification rather than a bool.
// A rejection is settled — the caller falls through to a normal review. A failed
// read is transient — the caller must emit nothing and retry on the next poll.
//
// Collapsing the two was a defect. A review emitted for a SHA that would later
// have qualified writes a review-task cursor entry, and the
// review-already-emitted guard then blocks the override for that SHA
// *permanently*, so one failed read cost a full LLM review forever. Observed
// 2026-10-25 on bborbe/errors#22: deferred by the webhook at 21:13:37, reviewed
// by the poll path at 21:15:39 — two minutes after the PR was created, which is
// precisely when a file listing or maintainer-config read is most likely to be
// incomplete. The PR then merged on that LLM review, spending the reviewer pod
// this feature exists to save.
func (w *watcher) qualifiesForMechanicalOverride(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) overrideQualification {
	if w.autoMergeLabel == "" {
		return overrideDoesNotQualify
	}
	if !slices.Contains(w.mechanicalOverrideAuthors, pr.AuthorLogin) {
		glog.V(3).Infof(
			"mechanical override: author not listed pr=%s/%s#%d login=%s",
			pr.Owner, pr.Repo, pr.Number, pr.AuthorLogin,
		)
		return overrideDoesNotQualify
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		glog.Errorf("mechanical override trust check failed pr=%s err=%v", pr.HTMLURL, err)
		return overrideIndeterminate
	}
	if !trustResult.Success() {
		glog.V(2).Infof("mechanical override skipped, untrusted author pr=%s", pr.HTMLURL)
		return overrideDoesNotQualify
	}
	cfg, err := w.ghClient.GetMaintainerConfig(ctx, pr.Owner, pr.Repo)
	if err != nil {
		glog.Errorf("mechanical override: get maintainer config failed pr=%s err=%v",
			pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return overrideIndeterminate
	}
	if !cfg.AutoMerge.Trivial || !cfg.PrReviewer.AutoApprove {
		glog.V(3).Infof(
			"mechanical override: repo not opted in pr=%s/%s",
			pr.Owner, pr.Repo,
		)
		return overrideDoesNotQualify
	}
	files, err := w.ghClient.ListPRFiles(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		glog.Errorf("mechanical override: list pr files failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return overrideIndeterminate
	}
	if !ClassifyTrivial(files) {
		glog.V(3).Infof(
			"mechanical override: not trivial pr=%s/%s#%d",
			pr.Owner, pr.Repo, pr.Number,
		)
		return overrideDoesNotQualify
	}
	return overrideQualifies
}

// BuildCreateCommand builds a CreateTaskCommand for a PR given its details and trust result.
// It is used by both the poll path (via PublishCreate) and the single-PR trigger handler.
//
// forced marks an operator-requested re-review (trigger with force=true). The caller has
// already salted taskIDStr via DeriveTaskIDForce; forced makes the TITLE differ too, which
// is what actually matters — the agent controller dedupes on title path, so a salted
// task_identifier alone lands on the existing review task's filename and is silently
// rejected with ErrTaskAlreadyExists (confirmed in prod, bborbe/coding#90, 2026-08-09).
func BuildCreateCommand(
	pr PullRequest,
	details PRDetails,
	taskIDStr string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
	trustResult trust.Result,
	forced bool,
	maxAdditions int,
	maxChangedFiles int,
	exclusion Exclusion,
) task.CreateCommand {
	retryToken := retryTokenFor(taskIDStr, forced)
	if trustResult.Success() {
		// Park oversized PRs at human_review instead of spawning a review pod:
		// a diff past the thresholds overflows the reviewer's context window
		// (claude CLI failed: Prompt is too long → compact_error: too_few_groups),
		// so the pod would die before posting any verdict — deterministic waste.
		// The parked task needs operator reassignment to proceed. A forced
		// re-review bypasses the park: the operator explicitly requested it.
		if !forced && oversized(details, exclusion, maxAdditions, maxChangedFiles) {
			return task.CreateCommand{
				Title: computePRTitle(
					"github",
					pr.Owner,
					pr.Repo,
					pr.Number,
					details.HeadSHA,
					pr.Title,
					maxSlugLen,
					maxTitleLen,
					taskSuffix,
					retryToken,
				),
				TargetVault:    targetVault,
				TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
				Frontmatter:    buildHumanReviewFrontmatter(pr, taskIDStr, stage, details),
				Body:           buildParkedBody(details, exclusion, maxAdditions, maxChangedFiles),
			}
		}
		return task.CreateCommand{
			Title: computePRTitle(
				"github",
				pr.Owner,
				pr.Repo,
				pr.Number,
				details.HeadSHA,
				pr.Title,
				maxSlugLen,
				maxTitleLen,
				taskSuffix,
				retryToken,
			),
			TargetVault:    targetVault,
			TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
			Frontmatter:    buildFrontmatter(pr, taskIDStr, stage, details),
			Body:           buildTaskBody(pr, exclusion),
		}
	}
	author := pr.AuthorLogin
	if author == "" {
		author = "(unknown)"
	}
	return task.CreateCommand{
		Title: computePRTitle(
			"github",
			pr.Owner,
			pr.Repo,
			pr.Number,
			details.HeadSHA,
			pr.Title,
			maxSlugLen,
			maxTitleLen,
			taskSuffix,
			retryToken,
		),
		TargetVault:    targetVault,
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildHumanReviewFrontmatter(pr, taskIDStr, stage, details),
		Body:           buildUntrustedBody(author, trustResult.Description()),
	}
}

// retryTokenFor derives the title-level retry marker for a forced re-review.
// It reuses the first 8 characters of the already-salted taskIDStr so the vault
// filename points straight at its own task_identifier, and matches the 8-char
// short-SHA segment already in the title. Returns "" when not forced, keeping
// every non-forced title byte-identical to what shipped before.
func retryTokenFor(taskIDStr string, forced bool) string {
	if !forced {
		return ""
	}
	if len(taskIDStr) > 8 {
		return taskIDStr[:8]
	}
	return taskIDStr
}

// BuildOverrideCommand builds a `pr-override` CreateTaskCommand for a labeled,
// trusted-author PR. The caller (PublishOverride) guarantees trust, so there is
// no untrusted branch here. The title uses the "Override" kind so it never
// collides with the review task's vault file for the same (PR, SHA).
func BuildOverrideCommand(
	pr PullRequest,
	details PRDetails,
	taskIDStr string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
) task.CreateCommand {
	return task.CreateCommand{
		Title: computeTaskTitle(
			"Override",
			"github",
			pr.Owner,
			pr.Repo,
			pr.Number,
			details.HeadSHA,
			pr.Title,
			maxSlugLen,
			maxTitleLen,
			taskSuffix,
		),
		TargetVault:    targetVault,
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildOverrideFrontmatter(pr, taskIDStr, stage, details),
		Body:           buildOverrideBody(pr),
	}
}

// BuildMechanicalOverrideCommand builds the `pr-override` CreateTaskCommand for
// a qualifying mechanical override. It reuses BuildOverrideCommand for the
// title, task identifier, phase and clone fields — so the task path stays
// identical to the label override and the controller's title-path dedupe
// collapses the two — then stamps the reason and replaces the body.
func BuildMechanicalOverrideCommand(
	pr PullRequest,
	details PRDetails,
	taskIDStr string,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	targetVault string,
) task.CreateCommand {
	cmd := BuildOverrideCommand(
		pr, details, taskIDStr, stage, maxSlugLen, maxTitleLen, taskSuffix, targetVault,
	)
	cmd.Frontmatter["override_reason"] = overrideReasonTrivialBotBump
	cmd.Body = buildMechanicalOverrideBody(pr)
	return cmd
}

func (w *watcher) fetchPRDetails(
	ctx context.Context,
	pr PullRequest,
	cache map[string]PRDetails,
) (PRDetails, error) {
	cacheKey := fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	if details, ok := cache[cacheKey]; ok {
		return details, nil
	}
	details, err := w.ghClient.GetPRDetails(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return PRDetails{}, errors.Wrapf(
			ctx,
			err,
			"get pr details pr=%s/%s#%d",
			pr.Owner,
			pr.Repo,
			pr.Number,
		)
	}
	cache[cacheKey] = details
	return details, nil
}

func buildTaskBody(pr PullRequest, exclusion Exclusion) string {
	repoLink := fmt.Sprintf("https://github.com/%s/%s", pr.Owner, pr.Repo)
	return fmt.Sprintf(
		"# PR Review: %s\n\n%s\n\n**Repo:** [%s/%s](%s)\n%s",
		pr.Title,
		pr.HTMLURL,
		pr.Owner,
		pr.Repo,
		repoLink,
		buildExclusionNote(exclusion),
	)
}

func buildFrontmatter(
	pr PullRequest,
	taskIDStr, stage string,
	details PRDetails,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "pr-review",
		"assignee":        "pr-reviewer-agent",
		"phase":           "planning",
		"status":          "in_progress",
		"stage":           stage,
		"task_identifier": taskIDStr,
		"title":           pr.Title,
		"clone_url":       details.CloneURL,
		"ref":             details.HeadSHA,
		"base_ref":        details.BaseRef,
	}
}

func buildHumanReviewFrontmatter(
	pr PullRequest,
	taskIDStr, stage string,
	details PRDetails,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "pr-review",
		"assignee":        "",
		"phase":           "human_review",
		"status":          "todo",
		"stage":           stage,
		"task_identifier": taskIDStr,
		"title":           pr.Title,
		"clone_url":       details.CloneURL,
		"ref":             details.HeadSHA,
		"base_ref":        details.BaseRef,
	}
}

// Exclusion is a computed result, not a constructed dependency, so the
// functions producing it (ComputeExclusion, ReviewIgnoreExclusion) are named
// for the computation rather than carrying a `New` constructor prefix.
//
// Exclusion is what a repo's `.reviewignore` removed from the size-gate
// counts. The zero value means nothing was excluded — the state for a repo
// with no `.reviewignore`, and the safe default on any fetch error, so a
// failure to read the file can only ever park more, never less.
type Exclusion struct {
	// Additions is the sum of added lines across matched files.
	Additions int
	// Files is the number of matched files.
	Files int
}

// Empty reports whether the exclusion removed nothing, so callers can skip
// reporting a line that would read "0 additions across 0 files excluded".
func (e Exclusion) Empty() bool {
	return e.Additions == 0 && e.Files == 0
}

// ComputeExclusion sums the additions and file count that matcher excludes
// across the PR's changed files. `.reviewignore` can never match itself (the
// guard lives in reviewignore.Parse), so the file's own edits always count
// toward the gate and never appear here.
func ComputeExclusion(files []PRFile, matcher reviewignore.Matcher) Exclusion {
	if matcher == nil {
		return Exclusion{}
	}
	var e Exclusion
	for _, f := range files {
		if matcher.Match(f.Filename) {
			e.Additions += f.Additions
			e.Files++
		}
	}
	return e
}

// oversized reports whether a PR's REVIEWABLE diff exceeds the park
// thresholds. Reviewable means net of exclusion: whatever a repo's
// `.reviewignore` matched is subtracted first, so non-reviewable content
// (vendored deps, generated mocks, dark-factory pipeline state) cannot park a
// small code change. A threshold of 0 disables that dimension (never parks on
// it). Park when either dimension is STRICTLY over its limit — a PR exactly at
// the limit still reviews normally. The check is deliberately per-dimension
// disabled rather than treat-0-as-limit, so an unset env var cannot silently
// park every PR.
func oversized(
	details PRDetails,
	exclusion Exclusion,
	maxAdditions, maxChangedFiles int,
) bool {
	additions, changedFiles := effectiveSize(details, exclusion)
	if maxAdditions > 0 && additions > maxAdditions {
		return true
	}
	if maxChangedFiles > 0 && changedFiles > maxChangedFiles {
		return true
	}
	return false
}

// effectiveSize returns the PR's reviewable added-line and changed-file counts
// after subtracting the exclusion. Both are clamped at 0: GitHub's PR-level
// totals and its per-file list are fetched in separate calls and can disagree
// on a PR that changed between them, and a negative count would silently
// disable the gate.
func effectiveSize(details PRDetails, exclusion Exclusion) (int, int) {
	additions := max(details.Additions-exclusion.Additions, 0)
	changedFiles := max(details.ChangedFiles-exclusion.Files, 0)
	return additions, changedFiles
}

// buildParkedBody explains why a PR was parked at human_review and how the
// operator proceeds. Only dimensions with an enabled threshold are listed,
// so a disabled dimension never appears as a confusing "limit 0".
func buildParkedBody(
	details PRDetails,
	exclusion Exclusion,
	maxAdditions, maxChangedFiles int,
) string {
	additions, changedFiles := effectiveSize(details, exclusion)
	var reasons []string
	if maxAdditions > 0 {
		reasons = append(
			reasons,
			fmt.Sprintf("%d added lines (limit %d)", additions, maxAdditions),
		)
	}
	if maxChangedFiles > 0 {
		reasons = append(
			reasons,
			fmt.Sprintf("%d changed files (limit %d)", changedFiles, maxChangedFiles),
		)
	}
	return fmt.Sprintf(
		"## Oversized PR — parked for human review\n\nThis PR has %s, which exceeds the auto-reviewer's context budget. Spawning a review pod would overflow its context window (`claude CLI failed: Prompt is too long`) and die before posting a verdict.\n%s\nTo review anyway, request a forced re-review on github-pr-watcher: `/trigger?url=<this PR's GitHub URL>&force=true` (the endpoint needs no token). Build the URL from this record's `clone_url` frontmatter with the trailing `.git` replaced by `/pull/<number>`, where `<number>` is the PR number in this record's title (`PR Review github - <owner>-<repo> - <number> - …`). Do not paste the URL into this record. The forced run skips only the oversized park and composes a fresh, reviewable record — but it is suppressed for up to an hour after this record was composed: the parked publish counts as an in-flight review and this SHA carries no verdict, so the retry guard skips the forced run after the endpoint has already answered 202, and no record appears. Wait out that hour, or restart the watcher (the retry-guard ledger is in memory, and a restart clears it), then retry.\n\nEditing this record's frontmatter does not work: this body carries no PR URL, so the reviewer fails the record.\n\nTo dismiss, set `status: aborted`.\n",
		strings.Join(reasons, " and "),
		buildExclusionNote(exclusion),
	)
}

// buildExclusionNote renders the `.reviewignore` audit line that makes an
// exclusion visible rather than silent. It is deliberately present on BOTH the
// park body and the normal review body: reporting it only on parks would hide
// it on exactly the PRs the exclusion rescued from parking, which is the case
// the visibility control exists for. Returns an empty string when nothing was
// excluded, so repos without a `.reviewignore` see no change.
func buildExclusionNote(exclusion Exclusion) string {
	if exclusion.Empty() {
		return ""
	}
	return fmt.Sprintf(
		"\n%d additions across %d files excluded by `%s` (not counted toward the size gate, not sent to the reviewer).\n",
		exclusion.Additions,
		exclusion.Files,
		reviewignore.Filename,
	)
}

// buildOverrideFrontmatter builds the frontmatter for a `pr-override` task.
// task_type `pr-override` is what routes the task to the code-only override
// agent (which posts an APPROVE at head SHA). The phase is `execution` — a
// valid domain.TaskPhase (the vault-cli frontmatter validator rejects unknown
// phase literals); the override agent names its single phase `execution` to
// match. clone_url/ref/base_ref mirror the review task — the override agent
// needs `ref` (head SHA) to post at the right commit; the others are harmless.
func buildOverrideFrontmatter(
	pr PullRequest,
	taskIDStr, stage string,
	details PRDetails,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       "pr-override",
		"assignee":        "pr-reviewer-agent",
		"phase":           "execution",
		"status":          "in_progress",
		"stage":           stage,
		"task_identifier": taskIDStr,
		"title":           pr.Title,
		"clone_url":       details.CloneURL,
		"ref":             details.HeadSHA,
		"base_ref":        details.BaseRef,
	}
}

func buildOverrideBody(pr PullRequest) string {
	repoLink := fmt.Sprintf("https://github.com/%s/%s", pr.Owner, pr.Repo)
	return fmt.Sprintf(
		"# PR Override: %s\n\n%s\n\nA trusted author applied the override label. "+
			"The agent will post an APPROVE at head SHA so the false-positive review no longer blocks merge.\n\n"+
			"**Repo:** [%s/%s](%s)\n",
		pr.Title,
		pr.HTMLURL,
		pr.Owner,
		pr.Repo,
		repoLink,
	)
}

// overrideReasonTrivialBotBump is the frontmatter `override_reason` value that
// tells the review agent this override is mechanical: no LLM review ran, and
// the body names the rule. It is the only reason the watcher emits today.
const overrideReasonTrivialBotBump = "trivial-bot-bump"

// buildMechanicalOverrideBody renders the body for a mechanical override. It
// deliberately never mentions the override label: no human applied it, so
// reusing the label wording would misreport why the approve was posted.
func buildMechanicalOverrideBody(pr PullRequest) string {
	repoLink := fmt.Sprintf("https://github.com/%s/%s", pr.Owner, pr.Repo)
	return fmt.Sprintf(
		"# PR Override: %s\n\n%s\n\nMechanical approve: this PR qualifies as a "+
			"`trivial-bot-bump` — a bot dependency bump whose changed files are all "+
			"mechanical. No LLM review ran. The watcher applied the auto-merge label "+
			"and armed GitHub-native auto-merge; GitHub merges once required checks "+
			"are green.\n\n**Repo:** [%s/%s](%s)\n",
		pr.Title,
		pr.HTMLURL,
		pr.Owner,
		pr.Repo,
		repoLink,
	)
}

func buildUntrustedBody(author, reasons string) string {
	return fmt.Sprintf(
		"## Untrusted author\n\nThis PR is by GitHub user **%s** which did not pass the trust check:\n\n- %s\n\nThis record cannot be made reviewable from here. Editing the frontmatter fails, because this body carries no PR URL. A forced re-trigger also fails, because force skips only the oversized park and not the trust check.\n\nTo have this author's PRs reviewed, add the login to the watcher's `TRUSTED_AUTHORS` list and restart the watcher — the list is read at startup, so the change takes effect only after a restart. After that, the next push (new SHA), or a forced `/trigger`, creates a reviewable record — but a forced run within an hour of this record being composed is suppressed as in-flight, because the parked publish counts as an in-flight review and this SHA carries no verdict; wait out that hour, or restart the watcher again, before forcing. A plain `/trigger` for this same SHA does not create anything: it composes the same title as this record, so the controller's create-if-not-exists dedupes it. If this PR is also oversized, a new push parks again, so only the forced `/trigger` path applies.\n\nTo dismiss, set `status: aborted`.\n",
		author,
		reasons,
	)
}
