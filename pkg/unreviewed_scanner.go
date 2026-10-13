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
	// reviewer-bot review on its current head SHA before it is reported.
	Threshold libtime.Duration
}

// ParseUnreviewedConfig parses the raw REVIEWER_BOT_LOGINS and
// UNREVIEWED_THRESHOLD values into an UnreviewedConfig. Logins are split like
// BOT_ALLOWLIST (trimmed, empties dropped); the threshold must parse as a
// positive duration. No logins after parsing means the feature is disabled.
func ParseUnreviewedConfig(
	ctx context.Context,
	rawLogins, rawThreshold string,
) (UnreviewedConfig, error) {
	threshold, err := parseUnreviewedThreshold(ctx, rawThreshold)
	if err != nil {
		return UnreviewedConfig{}, err
	}
	return UnreviewedConfig{
		Logins:    ParseBotAllowlist(rawLogins),
		Threshold: threshold,
	}, nil
}

// parseUnreviewedThreshold parses raw as a positive libtime.Duration. Unlike
// parseMaxPRAge (whose error text names MAX_PR_AGE and accepts 0 as
// "disabled"), the unreviewed scan needs a real wait, so zero and negative
// values are rejected.
func parseUnreviewedThreshold(ctx context.Context, raw string) (libtime.Duration, error) {
	parsed, err := libtime.ParseDuration(ctx, raw)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "parse UNREVIEWED_THRESHOLD")
	}
	if parsed == nil || *parsed <= 0 {
		return 0, errors.Errorf(ctx, "UNREVIEWED_THRESHOLD must be positive, got %q", raw)
	}
	return *parsed, nil
}

//counterfeiter:generate -o ../mocks/unreviewed_scanner.go --fake-name UnreviewedScanner . UnreviewedScanner

// UnreviewedScanner reports, once per poll cycle, the open pull requests the
// watcher would hand to the reviewer whose current head SHA still has no
// review from a configured reviewer bot.
//
// Scan deliberately returns no error: a failed or interrupted scan must never
// fail the poll cycle. It leaves the previous gauge values in place instead,
// so a scan that keeps failing is visible through the separate
// last-successful-scan timestamp rather than through a broken poll.
type UnreviewedScanner interface {
	Scan(ctx context.Context)
}

// NewUnreviewedScanner returns an UnreviewedScanner backed by the shared
// GitHub client, metrics and decision chains the watcher already holds, so the
// scan applies exactly the same filters, trust check and park decision as the
// publish path.
func NewUnreviewedScanner(
	ghClient GitHubClient,
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
		metrics:            metrics,
		currentDateTime:    currentDateTime,
		scope:              scope,
		taskCreationFilter: taskCreationFilter,
		trustDecision:      trustDecision,
		overrideLabel:      overrideLabel,
		maxAdditions:       maxAdditions,
		maxChangedFiles:    maxChangedFiles,
		cfg:                cfg,
	}
}

type unreviewedScanner struct {
	// mu serialises Scan. /check runs Poll through a background handler whose
	// ParallelSkipper de-duplicates /check calls only, not /check against the
	// ticker, so two scans could otherwise interleave and publish a mix of
	// both results.
	mu                 sync.Mutex
	ghClient           GitHubClient
	metrics            Metrics
	currentDateTime    libtime.CurrentDateTimeGetter
	scope              string
	taskCreationFilter filter.TaskCreationFilter
	trustDecision      trust.Trust
	overrideLabel      string
	maxAdditions       int
	maxChangedFiles    int
	cfg                UnreviewedConfig
}

// Scan fetches every open PR in scope, qualifies each one, and publishes the
// resulting set — possibly empty — on the unreviewed-PR gauge. Any failure or
// cancellation returns without touching the metric, leaving the previous
// values in place.
func (s *unreviewedScanner) Scan(ctx context.Context) {
	if len(s.cfg.Logins) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	prs, ok := s.fetchAllOpenPRs(ctx)
	if !ok {
		return
	}
	now := s.currentDateTime.Now()
	unreviewed := make([]UnreviewedPR, 0, len(prs))
	for _, pr := range prs {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("unreviewed scan cancelled during pr loop")
			return
		default:
		}
		counted, err := s.qualify(ctx, pr, now)
		if err != nil {
			glog.Warningf("unreviewed scan aborted err=%v", err)
			return
		}
		if !counted {
			continue
		}
		unreviewed = append(unreviewed, UnreviewedPR{
			Repo:   pr.Owner + "/" + pr.Repo,
			Number: pr.Number,
		})
	}
	s.metrics.SetUnreviewedPRs(unreviewed, now)
	glog.V(2).Infof("unreviewed scan found %d prs: %s",
		len(unreviewed), formatUnreviewedPRs(unreviewed))
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

// qualify reports whether a single open PR should be reported as unreviewed.
// The bool is true only when the PR is one PublishCreate would hand to the
// reviewer AND no review by a configured reviewer login targets its current
// head SHA. A non-nil error aborts the whole scan; a false bool with a nil
// error means "not counted" and the scan continues.
func (s *unreviewedScanner) qualify(
	ctx context.Context,
	pr PullRequest,
	now libtime.DateTime,
) (bool, error) {
	if s.taskCreationFilter.Skip(filter.PR{
		AuthorLogin: pr.AuthorLogin,
		IsDraft:     pr.IsDraft,
		Title:       pr.Title,
		UpdatedAt:   pr.UpdatedAt,
		RepoKey:     "github.com/" + pr.Owner + "/" + pr.Repo,
	}) {
		return false, nil
	}
	// A PR carrying the override label gets a `pr-override` task instead of a
	// bot review, so it is never expected to be reviewed by the bot.
	if s.overrideLabel != "" && slices.Contains(pr.Labels, s.overrideLabel) {
		return false, nil
	}
	trustResult, err := s.trustDecision.IsTrusted(ctx, trust.PR{AuthorLogin: pr.AuthorLogin})
	if err != nil {
		glog.Warningf("unreviewed scan: trust check failed pr=%s err=%v", pr.HTMLURL, err)
		return false, nil
	}
	if !trustResult.Success() {
		return false, nil
	}
	// UpdatedAt is the reviewable-since clock: it moves on open, on a new push
	// and on draft→ready, each of which restarts the wait.
	if now.Sub(pr.UpdatedAt) < s.cfg.Threshold {
		return false, nil
	}
	details, err := s.ghClient.GetPRDetails(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return false, err
	}
	// Fail-closed, mirroring processPR: no head SHA means no reviewable commit.
	if details.HeadSHA == "" {
		return false, nil
	}
	if s.parked(ctx, pr, details) {
		return false, nil
	}
	reviews, err := s.ghClient.ListReviews(ctx, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return false, err
	}
	for _, review := range reviews {
		if review.CommitID == details.HeadSHA &&
			matchesReviewerLogin(review.AuthorLogin, s.cfg.Logins) {
			return false, nil
		}
	}
	return true, nil
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
