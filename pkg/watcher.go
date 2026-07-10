// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"slices"

	agentlib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
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
) TaskPublisher {
	return &taskPublisher{
		createSender:  createSender,
		trustDecision: trustDecision,
		metrics:       metrics,
		cfg:           cfg,
	}
}

type taskPublisher struct {
	createSender  task.CreateCommandSender
	trustDecision trust.Trust
	metrics       Metrics
	cfg           TaskConfig
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
	)

	if err := p.createSender.SendCommand(ctx, cmd); err != nil {
		glog.Errorf("publish create-task failed pr=%s err=%v", pr.HTMLURL, err)
		p.metrics.IncPRPublished("error")
		return false
	}
	glog.V(2).Infof("published CreateTaskCommand pr=%s/%s#%d sha=%s taskID=%s trusted=%t",
		pr.Owner, pr.Repo, pr.Number, details.HeadSHA, taskIDStr, trustResult.Success())
	p.metrics.IncPRPublished("create")
	return true
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
) Watcher {
	return &watcher{
		ghClient:           ghClient,
		publisher:          publisher,
		metrics:            metrics,
		cursorPath:         cursorPath,
		startTime:          startTime,
		scope:              scope,
		taskCreationFilter: taskCreationFilter,
		overrideLabel:      overrideLabel,
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
}

func (w *watcher) Poll(ctx context.Context) error {
	cursorState, err := LoadCursor(ctx, w.cursorPath, w.startTime)
	if err != nil {
		return errors.Wrapf(ctx, err, "load cursor")
	}

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

// BuildCreateCommand builds a CreateTaskCommand for a PR given its details and trust result.
// It is used by both the poll path (via PublishCreate) and the single-PR trigger handler.
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
) task.CreateCommand {
	if trustResult.Success() {
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
			),
			TargetVault:    targetVault,
			TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
			Frontmatter:    buildFrontmatter(pr, taskIDStr, stage, details),
			Body:           buildTaskBody(pr),
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
		),
		TargetVault:    targetVault,
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter:    buildHumanReviewFrontmatter(pr, taskIDStr, stage, details),
		Body:           buildUntrustedBody(author, trustResult.Description()),
	}
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

func buildTaskBody(pr PullRequest) string {
	repoLink := fmt.Sprintf("https://github.com/%s/%s", pr.Owner, pr.Repo)
	return fmt.Sprintf(
		"# PR Review: %s\n\n%s\n\n**Repo:** [%s/%s](%s)\n",
		pr.Title,
		pr.HTMLURL,
		pr.Owner,
		pr.Repo,
		repoLink,
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
