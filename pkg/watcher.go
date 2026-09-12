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
}

// NewTaskPublisher returns a TaskPublisher that performs trust evaluation
// then publishes a CreateTaskCommand via the given CreateCommandSender.
func NewTaskPublisher(
	createSender task.CreateCommandSender,
	trustDecision trust.Trust,
	metrics Metrics,
	cfg TaskConfig,
	ghClient GitHubClient,
) TaskPublisher {
	return &taskPublisher{
		createSender:  createSender,
		trustDecision: trustDecision,
		metrics:       metrics,
		cfg:           cfg,
		ghClient:      ghClient,
	}
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
}

// PublishCreate implements TaskPublisher.
func (p *taskPublisher) PublishCreate(
	ctx context.Context,
	pr PullRequest,
	taskIDStr string,
	details PRDetails,
) bool {
	author := pr.AuthorLogin

	trustResult, err := p.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: author})
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
	return true
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
	trustResult, err := p.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})
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

// NewWatcher returns a Watcher that polls GitHub and publishes commands.
func NewWatcher(
	ghClient GitHubClient,
	publisher TaskPublisher,
	metrics Metrics,
	cursorPath string,
	startTime libtime.DateTime,
	scope string,
	taskCreationFilter filter.TaskCreationFilter,
	overrideLabel string,
	autoMergeLabel string,
	trivialAutoMergeEnabled bool,
	trustDecision trust.Trust,
) Watcher {
	return &watcher{
		ghClient:                ghClient,
		publisher:               publisher,
		metrics:                 metrics,
		cursorPath:              cursorPath,
		startTime:               startTime,
		scope:                   scope,
		taskCreationFilter:      taskCreationFilter,
		overrideLabel:           overrideLabel,
		autoMergeLabel:          autoMergeLabel,
		trivialAutoMergeEnabled: trivialAutoMergeEnabled,
		trustDecision:           trustDecision,
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
	// kill switch in addition to the per-repo `.maintainer.yaml` gate.
	trivialAutoMergeEnabled bool
	// trustDecision gates auto-merge arming to trusted authors (the label
	// alone is insufficient — an untrusted author must not be able to opt
	// any PR into auto-merge).
	trustDecision trust.Trust
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

	maxUpdatedAt := w.processPRs(ctx, &cursorState, allPRs)

	if maxUpdatedAt.After(cursorState.LastUpdatedAt) {
		cursorState.LastUpdatedAt = maxUpdatedAt
	}

	if err := SaveCursor(ctx, w.cursorPath, cursorState); err != nil {
		glog.Errorf("failed to save cursor err=%v", err)
	}
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

// processPRs iterates over fetched PRs, publishes commands, and returns the max updated-at seen.
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
) libtime.DateTime {
	maxUpdatedAt := cursorState.LastUpdatedAt
	prDetailsCache := make(map[string]PRDetails)
	newHeadSHAs := make(map[string]string, len(allPRs))

	for _, pr := range allPRs {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("poll cancelled during processPRs at pr %d", pr.Number)
			return maxUpdatedAt
		default:
		}

		if updatedAt, ok := w.processPR(ctx, pr, cursorState, newHeadSHAs, prDetailsCache); ok {
			if updatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = updatedAt
			}
		}
	}

	cursorState.HeadSHAs = newHeadSHAs
	return maxUpdatedAt
}

// processPR processes a single PR: filter → fetch details → override-or-review.
// It returns (pr.UpdatedAt, true) when the PR advanced cursor state (override
// emitted, review deduped, or review published) and (zero, false) when the PR
// was skipped (filtered, details error, empty SHA, or publish failure) and must
// not advance the cursor. cursorState and newHeadSHAs are mutated in place.
func (w *watcher) processPR(
	ctx context.Context,
	pr PullRequest,
	cursorState *Cursor,
	newHeadSHAs map[string]string,
	prDetailsCache map[string]PRDetails,
) (libtime.DateTime, bool) {
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
		return libtime.DateTime{}, false
	}

	details, err := w.fetchPRDetails(ctx, pr, prDetailsCache)
	if err != nil {
		glog.Errorf("get pr details failed pr=%s/%s#%d err=%v", pr.Owner, pr.Repo, pr.Number, err)
		// Same rationale as filtered PRs: cannot preserve old SHA-based entry without
		// knowing the SHA. Transient error → re-publish on next poll → controller deduplicates.
		return libtime.DateTime{}, false
	}

	// Fail-closed: if head SHA is absent, skip this PR on this poll.
	if details.HeadSHA == "" {
		glog.Warningf("missing head SHA for pr=%s/%s#%d, skipping", pr.Owner, pr.Repo, pr.Number)
		return libtime.DateTime{}, false
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
	w.tryAutoMerge(ctx, pr)

	// Override path: a trusted author carrying the override label gets a
	// `pr-override` task INSTEAD of a review task for this SHA. Emitting both
	// would race — the review could post CHANGES_REQUESTED after the override's
	// APPROVE and re-block merge. tryOverride returns false (no label, untrusted
	// author, or send error) → fall through to the normal review path.
	if w.tryOverride(ctx, pr, details, cursorState.HeadSHAs, newHeadSHAs) {
		return pr.UpdatedAt, true
	}

	taskIDStr := DeriveTaskID(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()

	if _, exists := cursorState.HeadSHAs[taskIDStr]; exists {
		// Same (PR, SHA) already spawned — no-op.
		glog.V(3).Infof(
			"no change, skipping pr=%s/%s#%d sha=%s taskID=%s",
			pr.Owner, pr.Repo, pr.Number, details.HeadSHA, taskIDStr,
		)
		newHeadSHAs[taskIDStr] = details.HeadSHA
		return pr.UpdatedAt, true
	}

	// New (PR, SHA) pair — publish a fresh CreateTaskCommand.
	if w.publisher.PublishCreate(ctx, pr, taskIDStr, details) {
		// Update cursorState in-place so duplicate PR entries in the same poll batch
		// are deduplicated without a second create publish.
		cursorState.HeadSHAs[taskIDStr] = details.HeadSHA
		newHeadSHAs[taskIDStr] = details.HeadSHA
		return pr.UpdatedAt, true
	}

	return libtime.DateTime{}, false
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
// from a trusted author whose branch has gone stale against its base. It
// returns true when the branch was updated.
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
// returns false so the caller continues normally; the conflicting-dep-bump
// case belongs to the update-go abort-and-reemit policy, not here.
//
// Failures never block the review path: like tryAutoMerge this is a side
// effect, and the caller continues regardless.
func (w *watcher) tryUpdateBranch(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) bool {
	if w.autoMergeLabel == "" || !slices.Contains(pr.Labels, w.autoMergeLabel) {
		return false
	}
	// A dirty dep-bump PR is the abort-and-reemit case, not a refresh case —
	// merging master in cannot resolve it (see isSupersededDepBump). Let the
	// abort path own that PR outright rather than attempting an update that
	// will 422.
	if isSupersededDepBump(pr, details) {
		return false
	}
	if !staleMergeState(details.MergeableState) {
		return false
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})
	if err != nil {
		glog.Errorf("update-branch trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	if !trustResult.Success() {
		glog.V(2).Infof("update-branch skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("update_branch_skipped")
		return false
	}
	if err := w.ghClient.UpdateBranch(ctx, pr.Owner, pr.Repo, pr.Number); err != nil {
		// Expected for a genuinely conflicting (`dirty`) PR — update-branch
		// resolves staleness, not conflicts. Logged, not fatal.
		glog.V(2).Infof("update branch failed pr=%s/%s#%d state=%s err=%v",
			pr.Owner, pr.Repo, pr.Number, details.MergeableState, err)
		w.metrics.IncPRPublished("update_branch_failed")
		return false
	}
	glog.V(2).Infof("updated branch pr=%s/%s#%d state=%s",
		pr.Owner, pr.Repo, pr.Number, details.MergeableState)
	w.metrics.IncPRPublished("update_branch")
	return true
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
// current master. Returns true when the PR was closed.
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
// Failures never block the review path: this returns false and the caller
// continues.
func (w *watcher) trySupersedeDepBump(
	ctx context.Context,
	pr PullRequest,
	details PRDetails,
) bool {
	if !isSupersededDepBump(pr, details) {
		return false
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})
	if err != nil {
		glog.Errorf("abort-and-reemit trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	if !trustResult.Success() {
		glog.V(2).Infof("abort-and-reemit skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("dep_bump_skipped")
		return false
	}
	if err := w.ghClient.ClosePR(
		ctx, pr.Owner, pr.Repo, pr.Number, depBumpSupersededComment,
	); err != nil {
		glog.Errorf("close superseded dep-bump pr failed pr=%s/%s#%d err=%v",
			pr.Owner, pr.Repo, pr.Number, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	glog.V(2).Infof("closed superseded dep-bump pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	w.metrics.IncPRPublished("dep_bump_superseded")
	return true
}

// tryAutoMerge arms GitHub-native auto-merge for a labeled PR from a trusted
// author. It returns true when the PR was armed (label present + trusted).
// It returns false — leaving the caller to continue normally — when the
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
) bool {
	if w.autoMergeLabel == "" || !slices.Contains(pr.Labels, w.autoMergeLabel) {
		return false
	}
	trustResult, err := w.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})
	if err != nil {
		glog.Errorf("auto-merge trust check failed pr=%s err=%v", pr.HTMLURL, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	if !trustResult.Success() {
		glog.V(2).Infof("auto-merge skipped, untrusted author pr=%s", pr.HTMLURL)
		w.metrics.IncPRPublished("auto_merge_skipped")
		return false
	}
	if err := w.ghClient.EnableAutoMerge(ctx, pr.Owner, pr.Repo, pr.Number); err != nil {
		glog.Errorf("enable auto-merge failed pr=%s/%s#%d err=%v",
			pr.Owner, pr.Repo, pr.Number, err)
		w.metrics.IncPRPublished("error")
		return false
	}
	glog.V(2).Infof("armed auto-merge pr=%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
	w.metrics.IncPRPublished("auto_merge")
	return true
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
		"## Oversized PR — parked for human review\n\nThis PR has %s, which exceeds the auto-reviewer's context budget. Spawning a review pod would overflow its context window (`claude CLI failed: Prompt is too long`) and die before posting a verdict.\n%s\nTo review anyway, edit the frontmatter: `assignee: pr-reviewer-agent`, `phase: in_progress`, `status: in_progress`. To dismiss, set `status: aborted`.\n",
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

func buildUntrustedBody(author, reasons string) string {
	return fmt.Sprintf(
		"## Untrusted author\n\nThis PR is by GitHub user **%s** which did not pass the trust check:\n\n- %s\n\nTo auto-process this PR, edit the frontmatter above:\n- `phase: in_progress`\n- `status: in_progress`\n\nTo dismiss, set `status: aborted`.\n",
		author,
		reasons,
	)
}
