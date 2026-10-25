// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// UnreviewedConfig configures the unreviewed-PR scan.
type UnreviewedConfig struct {
	// Logins are the GitHub logins whose reviews count as "the reviewer
	// looked at this PR". Matching ignores casing and a trailing `[bot]`
	// suffix, so `octopus-pr-reviewer` matches the App login
	// `octopus-pr-reviewer[bot]`. An empty list disables the scan entirely.
	Logins []string
	// Threshold is how long a publishable PR must have gone without a
	// reviewer-bot review on its current head SHA before it is reported, and
	// how long to wait between retries of an unreviewed PR.
	Threshold libtime.Duration
	// RetryMax is the most review tasks the scan re-publishes for one
	// (PR, head SHA) pair before it gives up and marks the PR exhausted. 0
	// disables retrying — every unreviewed PR is reported as exhausted.
	RetryMax int
}

// ParseUnreviewedConfig parses the raw REVIEWER_BOT_LOGINS, UNREVIEWED_THRESHOLD
// and UNREVIEWED_RETRY_MAX values into an UnreviewedConfig. Logins are split
// like BOT_ALLOWLIST (trimmed, empties dropped). No logins after parsing means
// the feature is disabled: the threshold is neither parsed nor validated and
// RetryMax is not range-validated — both are ignored and the returned
// Threshold is zero. With logins configured the threshold must parse as a
// positive duration.
func ParseUnreviewedConfig(
	ctx context.Context,
	rawLogins, rawThreshold string,
	retryMax int,
) (UnreviewedConfig, error) {
	logins := ParseBotAllowlist(rawLogins)
	if len(logins) == 0 {
		return UnreviewedConfig{RetryMax: retryMax}, nil
	}
	if retryMax < 0 {
		return UnreviewedConfig{}, errors.Errorf(
			ctx, "UNREVIEWED_RETRY_MAX must not be negative, got %d", retryMax,
		)
	}
	threshold, err := parseUnreviewedThreshold(ctx, rawThreshold)
	if err != nil {
		return UnreviewedConfig{}, err
	}
	return UnreviewedConfig{
		Logins:    logins,
		Threshold: threshold,
		RetryMax:  retryMax,
	}, nil
}

// parseUnreviewedThreshold parses raw as a positive libtime.Duration. Unlike
// parseMaxPRAge (whose error text names MAX_PR_AGE and accepts 0 as
// "disabled"), the unreviewed scan needs a real wait, so zero and negative
// values are rejected.
func parseUnreviewedThreshold(ctx context.Context, raw string) (libtime.Duration, error) {
	parsed, err := libtime.ParseDuration(ctx, raw)
	if err != nil {
		return 0, errors.Wrap(ctx, err, "parse UNREVIEWED_THRESHOLD")
	}
	if parsed == nil || *parsed <= 0 {
		return 0, errors.Errorf(ctx, "UNREVIEWED_THRESHOLD must be positive, got %q", raw)
	}
	return *parsed, nil
}

//counterfeiter:generate -o ../mocks/unreviewed_scanner.go --fake-name UnreviewedScanner . UnreviewedScanner

// UnreviewedScanner reports, once per poll cycle, the open pull requests the
// watcher would hand to the reviewer whose current head SHA still has no
// review from a configured reviewer bot, and re-publishes the original review
// task for those still unreviewed, a bounded number of times.
//
// It publishes only through TaskPublisher.PublishCreate with the deterministic
// DeriveTaskID, and never mutates GitHub — the scan is a read plus a re-send of
// an existing command, nothing more.
//
// Scan deliberately returns no error: a failed or interrupted scan must never
// fail the poll cycle. It leaves the previous gauge values in place instead,
// so a scan that keeps failing is visible through the separate
// last-successful-scan timestamp rather than through a broken poll.
type UnreviewedScanner interface {
	Scan(ctx context.Context)
}

// NewUnreviewedScanner returns an UnreviewedScanner backed by the shared
// GitHub client, publisher, metrics and decision chains the watcher already
// holds, so the scan applies exactly the same filters, trust check and park
// decision as the publish path, and re-publishes through the same publisher —
// never a second one, whose TaskConfig could drift and change the title path.
func NewUnreviewedScanner(
	ghClient GitHubClient,
	publisher TaskPublisher,
	metrics Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
	scope string,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	overrideLabel string,
	maxAdditions, maxChangedFiles int,
	cfg UnreviewedConfig,
) UnreviewedScanner {
	return &unreviewedScanner{
		ghClient:           ghClient,
		publisher:          publisher,
		metrics:            metrics,
		currentDateTime:    currentDateTime,
		scope:              scope,
		taskCreationFilter: taskCreationFilter,
		trustDecision:      trustDecision,
		overrideLabel:      overrideLabel,
		maxAdditions:       maxAdditions,
		maxChangedFiles:    maxChangedFiles,
		cfg:                cfg,
		retryState:         make(map[string]unreviewedRetry),
		reviewedCache:      make(map[string]libtime.DateTime),
	}
}

// unreviewedRetry is the bounded re-publish budget for one (PR, head SHA) task
// ID. prKey is retained because the task ID is a UUID5 and cannot be reversed
// into the `owner/repo#n` key used for pruning closed PRs.
type unreviewedRetry struct {
	prKey       string
	attempts    int
	lastAttempt libtime.DateTime
}

// unreviewedCandidate is one PR that qualified this scan, paired with the
// details the retry pass re-publishes unchanged — the title path depends on
// them.
type unreviewedCandidate struct {
	pr      PullRequest
	details PRDetails
}

type unreviewedScanner struct {
	// mu serialises Scan. /check runs Poll through a background handler whose
	// ParallelSkipper de-duplicates /check calls only, not /check against the
	// ticker, so two scans could otherwise interleave, publish a mix of both
	// results, and race on retryState (a fatal concurrent map write in Go).
	// An overlapping Scan is skipped, not queued: the next ticker poll runs a
	// fresh scan anyway, and queueing would only delay it.
	mu                 sync.Mutex
	ghClient           GitHubClient
	publisher          TaskPublisher
	metrics            Metrics
	currentDateTime    libtime.CurrentDateTimeGetter
	scope              string
	taskCreationFilter filter.TaskCreationFilter
	trustDecision      trust.Trust
	overrideLabel      string
	maxAdditions       int
	maxChangedFiles    int
	cfg                UnreviewedConfig
	// retryState is keyed by the task ID and holds the retry budget per
	// (PR, head SHA). In-memory only: a pod restart can grant up to RetryMax
	// more retries, which is acceptable. Guarded by mu.
	retryState map[string]unreviewedRetry
	// reviewedCache records PRs whose head SHA was confirmed reviewed by a
	// configured login, keyed by prKey, holding the UpdatedAt seen at that
	// confirmation. While UpdatedAt is unchanged, qualify skips the
	// GetPRDetails and ListReviews calls for that PR. Any push, draft toggle,
	// comment or label moves UpdatedAt, so a change always re-checks. Guarded
	// by mu.
	reviewedCache map[string]libtime.DateTime
}

// Scan fetches every open PR in scope, qualifies each one, and publishes the
// resulting set — possibly empty — on the unreviewed-PR gauge. A lookup
// failure (PR search, trust check, PR details or reviews), or cancellation
// before every PR is qualified, returns without touching the metric, leaving
// the previous values in place. Cancellation during the retry pass stops
// retrying but the completed scan still publishes its gauge.
func (s *unreviewedScanner) Scan(ctx context.Context) {
	if len(s.cfg.Logins) == 0 {
		return
	}
	if !s.mu.TryLock() {
		glog.V(2).Infof("unreviewed scan already running, skipping")
		return
	}
	defer s.mu.Unlock()

	prs, ok := s.fetchAllOpenPRs(ctx)
	if !ok {
		return
	}
	now := s.currentDateTime.Now()
	candidates := make([]unreviewedCandidate, 0, len(prs))
	for _, pr := range prs {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("unreviewed scan cancelled during pr loop")
			return
		default:
		}
		details, counted, err := s.qualify(ctx, pr, now)
		if err != nil {
			glog.Warningf("unreviewed scan aborted err=%v", err)
			return
		}
		if !counted {
			continue
		}
		candidates = append(candidates, unreviewedCandidate{pr: pr, details: details})
	}

	// Retry runs after qualification and before the gauge is published, so the
	// retries_exhausted label reflects this cycle's decisions. A cancelled
	// context stops retrying — the remaining PRs wait for the next scan — but
	// the completed scan still publishes its gauge.
	s.retry(ctx, candidates, now)

	unreviewed := make([]UnreviewedPR, 0, len(candidates))
	for _, c := range candidates {
		unreviewed = append(unreviewed, UnreviewedPR{
			Repo:             c.pr.Owner + "/" + c.pr.Repo,
			Number:           c.pr.Number,
			RetriesExhausted: s.retriesExhausted(c.pr, c.details, now),
		})
	}
	s.metrics.SetUnreviewedPRs(unreviewed, now)
	// Prune only on a complete scan, and only entries whose PR left the full
	// search result (closed or merged). Any other skip — threshold, filter,
	// empty SHA, park, a review arriving — must NOT reset the budget, or the
	// at-most-RetryMax-per-(PR, head SHA) bound breaks.
	s.pruneState(prs)
	if len(unreviewed) > 0 {
		glog.V(2).Infof("unreviewed scan found %d prs: %s",
			len(unreviewed), formatUnreviewedPRs(unreviewed))
	}
}

// retry re-publishes the original review task for each still-unreviewed
// candidate, up to RetryMax successful publishes per (PR, head SHA). It
// publishes only through TaskPublisher.PublishCreate with the deterministic
// DeriveTaskID — never the force path, never anything but the create command.
func (s *unreviewedScanner) retry(
	ctx context.Context,
	candidates []unreviewedCandidate,
	now libtime.DateTime,
) {
	for _, c := range candidates {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("unreviewed retry cancelled")
			return
		default:
		}
		taskID := DeriveTaskID(c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA).String()
		entry := s.retryState[taskID]
		if entry.attempts >= s.cfg.RetryMax {
			continue
		}
		if entry.attempts > 0 && now.Sub(entry.lastAttempt) < s.cfg.Threshold {
			continue
		}
		// A failed publish changes nothing — neither attempts nor lastAttempt —
		// so the PR is eligible again on the very next scan.
		if !s.publisher.PublishCreate(ctx, c.pr, taskID, c.details) {
			continue
		}
		s.retryState[taskID] = unreviewedRetry{
			prKey:       prKey(c.pr),
			attempts:    entry.attempts + 1,
			lastAttempt: now,
		}
		s.metrics.IncUnreviewedRetry(c.pr.Owner + "/" + c.pr.Repo)
		glog.Infof("unreviewed retry pr=%s/%s#%d sha=%s task_id=%s attempt=%d/%d",
			c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA, taskID,
			entry.attempts+1, s.cfg.RetryMax)
	}
}

// retriesExhausted reports whether the scan has given up on this PR: either
// retrying is disabled, or the budget is used up and the last retry has had a
// full threshold without a review arriving.
func (s *unreviewedScanner) retriesExhausted(
	pr PullRequest,
	details PRDetails,
	now libtime.DateTime,
) bool {
	if s.cfg.RetryMax == 0 {
		return true
	}
	entry := s.retryState[DeriveTaskID(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()]
	return entry.attempts >= s.cfg.RetryMax && now.Sub(entry.lastAttempt) >= s.cfg.Threshold
}

// pruneState drops retry and reviewed-cache entries whose PR is absent from
// this scan's full open-PR result, i.e. closed or merged. Entries for PRs that
// merely stopped qualifying this cycle are kept, so their budget survives.
func (s *unreviewedScanner) pruneState(prs []PullRequest) {
	open := make(map[string]struct{}, len(prs))
	for _, pr := range prs {
		open[prKey(pr)] = struct{}{}
	}
	for taskID, entry := range s.retryState {
		if _, ok := open[entry.prKey]; !ok {
			delete(s.retryState, taskID)
		}
	}
	for key := range s.reviewedCache {
		if _, ok := open[key]; !ok {
			delete(s.reviewedCache, key)
		}
	}
}

// fetchAllOpenPRs paginates the open-PR search. The bool is false when the
// scan must abort without publishing — cancellation or a search error — which
// fetchAllPRs cannot express, since it returns (nil, "") for both a cancelled
// scan and a genuinely empty result.
func (s *unreviewedScanner) fetchAllOpenPRs(ctx context.Context) ([]PullRequest, bool) {
	page := 1
	var allPRs []PullRequest
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("unreviewed scan cancelled before page search")
			return nil, false
		default:
		}
		result, err := s.ghClient.SearchOpenPRs(ctx, s.scope, page)
		if err != nil {
			glog.Warningf("unreviewed scan: search open prs failed err=%v", err)
			return nil, false
		}
		allPRs = append(allPRs, result.PullRequests...)
		if !result.HasNextPage {
			break
		}
		page = result.NextPage
	}
	return allPRs, true
}

// qualify reports whether a single open PR should be reported as unreviewed,
// returning the fetched details so the retry pass can re-publish unchanged.
// counted is true only when the PR is one PublishCreate would hand to the
// reviewer AND no review by a configured reviewer login targets its current
// head SHA. A non-nil error — a trust check, GetPRDetails or ListReviews
// failure — aborts the whole scan; a false counted with a nil error means
// "not counted" and the scan continues.
func (s *unreviewedScanner) qualify(
	ctx context.Context,
	pr PullRequest,
	now libtime.DateTime,
) (PRDetails, bool, error) {
	if s.taskCreationFilter.Skip(filter.PR{
		AuthorLogin: pr.AuthorLogin,
		IsDraft:     pr.IsDraft,
		Title:       pr.Title,
		UpdatedAt:   pr.UpdatedAt,
		RepoKey:     "github.com/" + pr.Owner + "/" + pr.Repo,
	}) {
		return PRDetails{}, false, nil
	}
	// A PR carrying the override label gets a `pr-override` task instead of a
	// bot review, so it is never expected to be reviewed by the bot.
	if s.overrideLabel != "" && slices.Contains(pr.Labels, s.overrideLabel) {
		return PRDetails{}, false, nil
	}
	// UpdatedAt is the reviewable-since clock: it moves on open, on a new push
	// and on draft→ready, each of which restarts the wait.
	if now.Sub(pr.UpdatedAt) < s.cfg.Threshold {
		return PRDetails{}, false, nil
	}
	// A settled review costs no GitHub calls until the PR changes.
	if s.cachedReviewed(pr) {
		return PRDetails{}, false, nil
	}
	details, err := s.ghClient.GetPRDetails(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return PRDetails{}, false, err
	}
	// The trust check runs after the details fetch because the head repo only
	// arrives with the details. For a trusted author this costs no extra GitHub
	// call — GetPRDetails was already made for every PR reaching this point. For
	// an untrusted author it costs one fetch per scan (cachedReviewed never
	// caches an untrusted PR) and a fetch error aborts the scan; both are
	// accepted costs of knowing the head repo. Do not move the fetch back above
	// this check.
	trustResult, err := s.trustDecision.IsTrusted(ctx, trust.PR{
		AuthorLogin:      pr.AuthorLogin,
		HeadRepoFullName: details.HeadRepoFullName,
		BaseRepoFullName: details.BaseRepoFullName,
	})
	if err != nil {
		return PRDetails{}, false, errors.Wrapf(ctx, err, "trust check %s", prKey(pr))
	}
	if !trustResult.Success() {
		return PRDetails{}, false, nil
	}
	// Fail-closed, mirroring processPR: no head SHA means no reviewable commit.
	if details.HeadSHA == "" {
		return PRDetails{}, false, nil
	}
	if s.parked(ctx, pr, details) {
		return PRDetails{}, false, nil
	}
	reviews, err := s.ghClient.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return PRDetails{}, false, err
	}
	for _, review := range reviews {
		if review.CommitID == details.HeadSHA &&
			matchesReviewerLogin(review.AuthorLogin, s.cfg.Logins) {
			s.reviewedCache[prKey(pr)] = pr.UpdatedAt
			return PRDetails{}, false, nil
		}
	}
	return details, true, nil
}

// cachedReviewed reports whether the PR's head SHA was already confirmed
// reviewed at this exact UpdatedAt, so qualify must not spend GetPRDetails and
// ListReviews on it again. A moved UpdatedAt invalidates the entry first, so
// the PR is re-checked. Only a confirmed review is ever cached — an
// unreviewed, skipped or errored PR is never recorded.
func (s *unreviewedScanner) cachedReviewed(pr PullRequest) bool {
	key := prKey(pr)
	cached, ok := s.reviewedCache[key]
	if !ok {
		return false
	}
	if cached.Equal(pr.UpdatedAt) {
		return true
	}
	delete(s.reviewedCache, key)
	return false
}

// parked reports whether PublishCreate would park this PR at human_review
// instead of handing it to the reviewer. It mirrors BuildCreateCommand: only a
// raw-oversized PR pays for the `.reviewignore` lookup, and an unreadable
// `.reviewignore` (ReviewIgnoreExclusion returns the zero Exclusion) parks,
// exactly as it does on the publish path.
func (s *unreviewedScanner) parked(ctx context.Context, pr PullRequest, details PRDetails) bool {
	if !oversized(details, Exclusion{}, s.maxAdditions, s.maxChangedFiles) {
		return false
	}
	exclusion := ReviewIgnoreExclusion(ctx, s.ghClient, pr.Owner, pr.Repo, pr.Number, pr.HTMLURL)
	return oversized(details, exclusion, s.maxAdditions, s.maxChangedFiles)
}

// matchesReviewerLogin reports whether a review's author login matches one of
// the configured reviewer logins. Both sides are normalised — lower-cased,
// then with a trailing `[bot]` suffix removed — so an App's API login and its
// configured form compare equal. An empty review login never matches.
func matchesReviewerLogin(reviewLogin string, logins []string) bool {
	normalized := normalizeReviewerLogin(reviewLogin)
	if normalized == "" {
		return false
	}
	for _, login := range logins {
		if normalizeReviewerLogin(login) == normalized {
			return true
		}
	}
	return false
}

// normalizeReviewerLogin lower-cases login and strips a trailing `[bot]`
// suffix, so `OCTOPUS-PR-REVIEWER[BOT]` and `octopus-pr-reviewer` compare equal.
func normalizeReviewerLogin(login string) string {
	return strings.TrimSuffix(strings.ToLower(login), botLoginSuffix)
}

// formatUnreviewedPRs renders the scan result as `owner/repo#n` entries for
// the V(2) log line.
func formatUnreviewedPRs(prs []UnreviewedPR) string {
	parts := make([]string, 0, len(prs))
	for _, pr := range prs {
		parts = append(parts, fmt.Sprintf("%s#%d", pr.Repo, pr.Number))
	}
	return strings.Join(parts, ", ")
}
