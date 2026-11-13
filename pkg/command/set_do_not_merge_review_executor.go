// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"strings"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/prurl"
)

// NewSetDoNotMergeReviewCommandExecutor creates a cdb.CommandObjectExecutorTx
// that consumes SetDoNotMergeReviewCommand messages and enforces the
// `do-not-merge` gate with a pull-request review: a CHANGES_REQUESTED review
// while the label is present, and a dismissal of that review once it is gone.
//
// It lives in the in-pod command consumer rather than the webhook handler for
// the same reason the status executor does: GitHub API access stays in one
// place and a failed call is retried, instead of turning a GitHub blip into a
// delivery GitHub never resends.
//
// Exit-path mapping:
//   - malformed payload / failed Validate  → cdb.ErrCommandObjectSkipped
//   - unparseable PR URL                   → cdb.ErrCommandObjectSkipped
//   - permanent GitHub error (4xx)         → cdb.ErrCommandObjectSkipped
//   - GitHub 5xx / network error           → wrapped error (transient, retried)
//   - success, or already in the wanted state → nil, nil, nil
//
// SendResultEnabled is false: the review is fire-and-forget, matching the
// trigger and status executors.
func NewSetDoNotMergeReviewCommandExecutor(
	ghClient pkg.GitHubClient,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		SetDoNotMergeReviewCommandOperation,
		false, // SendResultEnabled = false
		func(
			ctx context.Context,
			_ libkv.Tx,
			commandObject cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return runSetDoNotMergeReview(ctx, commandObject, ghClient)
		},
	)
}

func runSetDoNotMergeReview(
	ctx context.Context,
	commandObject cdb.CommandObject,
	ghClient pkg.GitHubClient,
) (*base.EventID, base.Event, error) {
	var cmd SetDoNotMergeReviewCommand
	if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"malformed SetDoNotMergeReviewCommand: %v",
			err,
		)
	}
	if err := cmd.Validate(ctx); err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"validate SetDoNotMergeReviewCommand: %v",
			err,
		)
	}
	prInfo, err := prurl.ParsePRURL(ctx, cmd.URL)
	if err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"parse url %q: %v",
			cmd.URL,
			err,
		)
	}

	// The reviews are read on BOTH paths. On the blocking path they are the
	// idempotency check (one gate review per PR, not one per webhook delivery);
	// on the releasing path they are what identifies the review to dismiss.
	reviews, err := ghClient.ListReviews(ctx, prInfo.Owner, prInfo.Repo, prInfo.Number)
	if err != nil {
		return nil, nil, errors.Wrapf(ctx, err, "list reviews %s", cmd.URL)
	}

	if cmd.Blocked() {
		return nil, nil, blockPR(ctx, ghClient, prInfo, cmd.URL, cmd.HeadSHA, reviews)
	}
	return nil, nil, releasePR(ctx, ghClient, prInfo, cmd.URL, reviews)
}

// blockPR submits the gate's CHANGES_REQUESTED review unless one is already
// standing.
//
// The check is on the gate's OWN reviews, not on "does a CHANGES_REQUESTED
// exist": the bot's genuine review may be CHANGES_REQUESTED for real findings,
// and mistaking that for the gate's review would leave a labelled PR with no
// gate review of its own — invisible, because the merge is blocked either way.
//
// An existing gate review blocks regardless of its commit id: GitHub does not
// clear a CHANGES_REQUESTED on a push, so re-posting on every synchronize
// would pile up identical reviews on a PR that is already held.
func blockPR(
	ctx context.Context,
	ghClient pkg.GitHubClient,
	prInfo *prurl.PRInfo,
	prURL string,
	headSHA string,
	reviews []pkg.Review,
) error {
	for _, review := range reviews {
		if isGateReview(review) && review.State == pkg.ReviewStateChangesRequested {
			glog.V(2).Infof(
				"do-not-merge review already standing url=%s review=%d",
				prURL,
				review.ID,
			)
			return nil
		}
	}
	if err := ghClient.SubmitReview(
		ctx,
		prInfo.Owner,
		prInfo.Repo,
		prInfo.Number,
		headSHA,
		DoNotMergeReviewBody,
		pkg.ReviewEventRequestChanges,
	); err != nil {
		return skipIfPermanent(ctx, err, "submit do-not-merge review", prURL)
	}
	glog.V(2).Infof("do-not-merge review submitted url=%s sha=%s", prURL, headSHA)
	return nil
}

// releasePR dismisses every standing gate review, and only those.
//
// Dismissing — rather than approving — is deliberate. An APPROVE would
// fabricate a verdict the reviewer never reached, and on a PR the bot had
// genuinely rejected it would erase a real finding. A dismissal instead
// restores whatever verdict the reviewer held before the label appeared: a PR
// the bot had approved returns to APPROVED and can merge, and one it never
// approved stays REVIEW_REQUIRED, which is the honest state.
func releasePR(
	ctx context.Context,
	ghClient pkg.GitHubClient,
	prInfo *prurl.PRInfo,
	prURL string,
	reviews []pkg.Review,
) error {
	for _, review := range reviews {
		if !isGateReview(review) || review.State != pkg.ReviewStateChangesRequested {
			continue
		}
		if err := ghClient.DismissReview(
			ctx,
			prInfo.Owner,
			prInfo.Repo,
			prInfo.Number,
			review.ID,
			DoNotMergeReviewDismissalMessage,
		); err != nil {
			return skipIfPermanent(ctx, err, "dismiss do-not-merge review", prURL)
		}
		glog.V(2).Infof("do-not-merge review dismissed url=%s review=%d", prURL, review.ID)
	}
	return nil
}

// isGateReview reports whether this mechanism posted the review, by its body
// marker. This is the whole scoping rule: without it the label-removal path
// would dismiss the reviewer bot's genuine verdicts.
func isGateReview(review pkg.Review) bool {
	return strings.HasPrefix(review.Body, DoNotMergeReviewMarker)
}

// skipIfPermanent maps a permanent GitHub failure to a skip so the consumer
// does not redeliver forever. It is loud on purpose: a permanent failure here
// is most often the App missing Pull requests: Write, which silently removes
// the fleet's only working way to hold a PR.
func skipIfPermanent(ctx context.Context, err error, action, prURL string) error {
	if errors.Is(err, pkg.ErrPermanent) {
		glog.Errorf("%s permanently failed, not retrying url=%s: %v", action, prURL, err)
		return errors.Wrapf(ctx, cdb.ErrCommandObjectSkipped, "permanent, not retrying: %v", err)
	}
	return errors.Wrapf(ctx, err, "%s %s", action, prURL)
}
