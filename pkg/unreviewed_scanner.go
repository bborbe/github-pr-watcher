// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
// task for those still unreviewed — a bounded number of retries, then one
// salted stale re-drive per hour for as long as no verdict arrives.
//
// It publishes only through the shared TaskPublisher — a bounded retry as
// PublishCreate with the deterministic DeriveTaskID, and a stale re-drive as
// PublishRedrive with a salted one — and never mutates GitHub. The scan is a
// read plus a re-send of an existing command, nothing more.
//
// Scan deliberately returns no error: a failed or interrupted scan must never
// fail the poll cycle. It leaves the previous gauge values in place instead,
// so a scan that keeps failing is visible through the separate
// last-successful-scan timestamp rather than through a broken poll.
type UnreviewedScanner interface {
	Scan(ctx context.Context)
	// LastCandidates returns how many PRs the last complete scan found still
	// owed a review. It is the population the watcher's silence line needs:
	// the raw search count is not it, because a PR sitting at the cursor
	// boundary is listed on every cycle and legitimately produces nothing.
	// Zero before the first complete scan and after a scan that aborted.
	LastCandidates() int
}

// NewUnreviewedScanner returns an UnreviewedScanner backed by the shared
// GitHub client, publisher, metrics and decision chains the watcher already
// holds, so the scan applies exactly the same filters, trust check and park
// decision as the publish path, and re-publishes through the same publisher —
// never a second one, whose TaskConfig could drift and change the title path.
func NewUnreviewedScanner(
	ghClient GitHubClient,
	publisher TaskPublisher,
	dispatchLedger DispatchLedger,
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
		dispatchLedger:     dispatchLedger,
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
	// publishedID is the task id the LAST publish used. It is not always the
	// deterministic DeriveTaskID: a stale re-drive publishes under a salted id,
	// and the task event — and therefore the dispatch mark — carries that one.
	// Counting against the deterministic id alone would leave a recovered task
	// reading as undispatched forever, which is the false signal this whole
	// instrument exists to remove.
	publishedID string
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
	dispatchLedger     DispatchLedger
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
	// lastCandidates is the size of the last complete scan's candidate set,
	// reported through LastCandidates. Atomic rather than mu-guarded: the
	// watcher reads it right after Scan, and Scan holds mu for its whole body.
	lastCandidates atomic.Int64
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

	// Recorded before the retry pass so the watcher's silence line reads this
	// cycle's population even when the retry pass is cancelled.
	s.lastCandidates.Store(int64(len(candidates)))

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
			RetriesExhausted: s.retriesExhausted(c.pr, c.details),
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

// LastCandidates implements UnreviewedScanner.
func (s *unreviewedScanner) LastCandidates() int {
	if s == nil {
		return 0
	}
	return int(s.lastCandidates.Load())
}

// retryOutcome is the single thing that happened to one candidate in one scan.
// Every candidate produces exactly one, so the counts reconcile against the
// population the scan listed and no PR can be listed and then dropped without a
// line saying what became of it.
type retryOutcome int

const (
	retryPublished retryOutcome = iota
	retrySuppressed
	retrySkipped
)

// retry re-publishes the review task for each still-unreviewed candidate, up to
// RetryMax successful publishes per (PR, head SHA), and — once that budget is
// spent and no legitimate run can still be in flight — re-drives the review on
// the stale clock instead of falling silent.
//
// It publishes only through the shared TaskPublisher: a bounded retry re-sends
// the deterministic DeriveTaskID through PublishCreate, and a stale re-drive
// sends a salted one through PublishRedrive (see retryCandidate).
func (s *unreviewedScanner) retry(
	ctx context.Context,
	candidates []unreviewedCandidate,
	now libtime.DateTime,
) {
	published, suppressed := 0, 0
	defer func() {
		if len(candidates) == 0 {
			return
		}
		// undispatched counts candidates THIS process published that never showed
		// a dispatch event. It is a SUBSET of published, not a fourth bucket, so
		// the sum invariant below still holds: a publish that dispatched is
		// counted in both.
		//
		// The process-lifetime scoping is what keeps it honest. retryState holds
		// only publishes made by this process, so a restarted pod reports zero
		// until it publishes something itself — it must not inherit another
		// process's publishes as undispatched, which would report a healthy
		// pipeline as broken and reproduce the original defect from the other
		// side.
		undispatched := s.countUndispatched(candidates)
		// Every candidate that was neither published nor suppressed was
		// skipped — including any the loop never reached because the context
		// was cancelled — so the three counts always sum to the population the
		// line reports.
		skipped := len(candidates) - published - suppressed
		glog.Infof(
			"unreviewed scan reconcile candidates=%d published=%d suppressed=%d skipped=%d undispatched=%d",
			len(candidates), published, suppressed, skipped, undispatched,
		)
	}()
	for _, c := range candidates {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("unreviewed retry cancelled")
			return
		default:
		}
		switch s.retryCandidate(ctx, c, now) {
		case retryPublished:
			published++
		case retrySuppressed:
			suppressed++
		case retrySkipped:
		}
	}
}

// countUndispatched reports how many candidates this process published and never
// observed being dispatched.
//
// The retryState lookup is the scoping, not an optimisation: it holds only
// publishes made by this process, so a pod that just started counts zero until it
// publishes something itself. Without it a restart would report every in-flight
// task as undispatched — the same false signal, inverted.
func (s *unreviewedScanner) countUndispatched(candidates []unreviewedCandidate) int {
	undispatched := 0
	for _, c := range candidates {
		taskID := DeriveTaskID(c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA).String()
		entry, publishedHere := s.retryState[taskID]
		if !publishedHere {
			continue
		}
		// The mark carries whichever id the last publish used — a stale re-drive
		// salts it — so check that id, falling back to the deterministic one for
		// an entry written before publishedID existed.
		marked := entry.publishedID
		if marked == "" {
			marked = taskID
		}
		if !s.dispatchLedger.Seen(marked) {
			undispatched++
		}
	}
	return undispatched
}

// retryCandidate re-publishes one still-unreviewed PR and reports exactly one
// outcome, logging the reason for anything that is not a publish.
//
// Two publish shapes, because they answer two different failures:
//
//   - Within the budget, the deterministic task id is re-sent. That heals a
//     command lost before the controller saw it, and cannot duplicate a live
//     run because the title is byte-identical to the one already accepted, so
//     the controller's title-path dedup drops it.
//   - Past the budget and past retryGuardStaleAfter, the record is dead: no
//     legitimate review run outlives that bound, and a verdict at the head SHA
//     would have removed the PR from the candidate set. A same-title re-send is
//     deduped by the controller and re-runs nothing, so the re-drive carries a
//     title the controller has not accepted before — PublishRedrive appends a
//     `retry-<token>` segment, and the salted task id gives that token a fresh
//     value. The title is the axis that matters: the controller dedups on the
//     title PATH, so salting the id alone was a no-op, measured on
//     bborbe/nuke#1104 whose re-publishes updated the task file without
//     restarting the job.
func (s *unreviewedScanner) retryCandidate(
	ctx context.Context,
	c unreviewedCandidate,
	now libtime.DateTime,
) retryOutcome {
	taskID := DeriveTaskID(c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA).String()
	entry := s.retryState[taskID]

	if s.cfg.RetryMax == 0 {
		glog.V(1).Infof("unreviewed skip pr=%s/%s#%d sha=%s reason=retries_disabled",
			c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA)
		return retrySkipped
	}

	if entry.attempts < s.cfg.RetryMax {
		if entry.attempts > 0 && now.Sub(entry.lastAttempt) < s.cfg.Threshold {
			glog.V(1).Infof("unreviewed skip pr=%s/%s#%d sha=%s reason=retry_backoff"+
				" retry_in=%s attempt=%d/%d",
				c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA,
				(s.cfg.Threshold - now.Sub(entry.lastAttempt)).String(),
				entry.attempts, s.cfg.RetryMax)
			return retrySkipped
		}
		return s.publishRetry(ctx, c, taskID, entry, now)
	}

	idle := now.Sub(entry.lastAttempt)
	// <=, not <: retryGuard.Check calls a record stale only when its age is
	// strictly greater than the bound, so at exactly one hour the guard still
	// considers it in flight. Re-driving one tick earlier would race that.
	if idle.Duration() <= retryGuardStaleAfter {
		glog.V(1).Infof("unreviewed skip pr=%s/%s#%d sha=%s reason=retries_exhausted"+
			" redrive_in=%s attempts=%d/%d",
			c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA,
			(retryGuardStaleAfter - idle.Duration()).String(), entry.attempts, s.cfg.RetryMax)
		return retrySkipped
	}

	return s.publishStaleRedrive(ctx, c, taskID, entry, now)
}

// publishRetry sends the bounded retry: the same deterministic task id the poll
// path published, so a command lost before the controller saw it is delivered
// again. A failed publish changes nothing — neither attempts nor lastAttempt —
// so the PR is eligible again on the very next scan.
func (s *unreviewedScanner) publishRetry(
	ctx context.Context,
	c unreviewedCandidate,
	taskID string,
	entry unreviewedRetry,
	now libtime.DateTime,
) retryOutcome {
	if !s.publisher.PublishCreate(ctx, c.pr, taskID, c.details) {
		glog.Infof("unreviewed suppress pr=%s/%s#%d sha=%s reason=publish_declined",
			c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA)
		return retrySuppressed
	}
	s.retryState[taskID] = unreviewedRetry{
		prKey:       prKey(c.pr),
		attempts:    entry.attempts + 1,
		lastAttempt: now,
		publishedID: taskID,
	}
	s.metrics.IncUnreviewedRetry(c.pr.Owner + "/" + c.pr.Repo)
	glog.Infof("unreviewed retry pr=%s/%s#%d sha=%s task_id=%s attempt=%d/%d",
		c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA, taskID,
		entry.attempts+1, s.cfg.RetryMax)
	return retryPublished
}

// publishStaleRedrive sends a salted re-drive for a record that has outlived
// retryGuardStaleAfter with no verdict at its head SHA. The attempt count is
// deliberately not reset: the budget stays spent (so the gauge keeps reporting
// the PR as exhausted) while lastAttempt moves, which paces the next re-drive
// one stale window later instead of letting it burst.
func (s *unreviewedScanner) publishStaleRedrive(
	ctx context.Context,
	c unreviewedCandidate,
	taskID string,
	entry unreviewedRetry,
	now libtime.DateTime,
) retryOutcome {
	idle := now.Sub(entry.lastAttempt)
	redriveID := DeriveTaskIDForce(
		c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA,
		strconv.FormatInt(now.Time().UnixNano(), 10),
	).String()
	// PublishRedrive, not PublishCreate: the salted id alone changes nothing,
	// because the controller dedups on the title path — the redrive method is
	// what puts the retry token in the title (and keeps the retry guard's
	// ledger untouched so an operator's forced /trigger still works).
	if !s.publisher.PublishRedrive(ctx, c.pr, redriveID, c.details) {
		glog.Infof("unreviewed suppress pr=%s/%s#%d sha=%s reason=publish_declined",
			c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA)
		return retrySuppressed
	}
	s.retryState[taskID] = unreviewedRetry{
		prKey:       prKey(c.pr),
		attempts:    entry.attempts,
		lastAttempt: now,
		publishedID: redriveID,
	}
	s.metrics.IncUnreviewedRetry(c.pr.Owner + "/" + c.pr.Repo)
	glog.Infof("unreviewed redrive stale record pr=%s/%s#%d sha=%s task_id=%s"+
		" stale_task_id=%s idle_seconds=%d",
		c.pr.Owner, c.pr.Repo, c.pr.Number, c.details.HeadSHA, redriveID, taskID,
		int64(idle.Duration()/time.Second))
	return retryPublished
}

// retriesExhausted reports whether this PR's bounded retry budget is spent.
// It is stable once spent — it does not flip back after a stale re-drive — so
// the alert on the gauge fires once and stays on while no review arrives,
// instead of flapping every threshold. Only a review at the head SHA, or a new
// commit, clears the series.
func (s *unreviewedScanner) retriesExhausted(
	pr PullRequest,
	details PRDetails,
) bool {
	if s.cfg.RetryMax == 0 {
		return true
	}
	entry := s.retryState[DeriveTaskID(pr.Owner, pr.Repo, pr.Number, details.HeadSHA).String()]
	return entry.attempts >= s.cfg.RetryMax
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
