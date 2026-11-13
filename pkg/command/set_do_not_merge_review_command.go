// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"

	"github.com/bborbe/maintainer/prurl"
)

// SetDoNotMergeReviewCommandOperation is the Kafka command operation for
// enforcing the `do-not-merge` gate through a pull-request review. Wire
// string: "set-do-not-merge-review".
//
// It rides the same GithubPRReviewV1SchemaID topic trio as the trigger and
// commit-status commands: the in-pod command consumer dispatches by operation,
// so no new schema ID and no `bborbe/maintainer` change are needed.
//
// This is the route the fleet actually uses to hold a PR. The commit-status
// route (SetDoNotMergeStatusCommandOperation) cannot work: the reviewer App
// declares no Commit statuses: Write, and an installation cannot grant a
// permission the App does not declare. A CHANGES_REQUESTED review blocks the
// merge on every ruleset that sets required_approving_review_count: 1, and
// Pull requests: Write is already held.
const SetDoNotMergeReviewCommandOperation base.CommandOperation = "set-do-not-merge-review"

// DoNotMergeReviewMarker opens the body of every review this mechanism posts.
//
// It is what makes the gate's reviews distinguishable from the reviewer bot's
// genuine verdicts on the same PR, so the label-removal path can dismiss its
// own review without erasing a real CHANGES_REQUESTED finding. An HTML comment
// renders invisibly, so the marker costs the human reader nothing.
const DoNotMergeReviewMarker = "<!-- do-not-merge-gate -->"

// DoNotMergeReviewBody is the body posted with the blocking review. The first
// line is the marker; the rest explains the hold to whoever reads the PR.
const DoNotMergeReviewBody = DoNotMergeReviewMarker + "\n\n" +
	"Held by the `do-not-merge` label. Remove the label to release this PR."

// DoNotMergeReviewDismissalMessage is recorded on the dismissal so the PR's
// timeline says why the block was lifted.
const DoNotMergeReviewDismissalMessage = "do-not-merge label removed"

// SetDoNotMergeReviewCommand is the payload for
// SetDoNotMergeReviewCommandOperation, published by the webhook handler on
// every pull_request delivery that can change the label state, and consumed
// by the in-pod command consumer.
//
// Labels is the PR's full label set as carried by the webhook payload. The
// executor derives the state from it rather than re-reading the PR: a second
// API read races a rapid label/unlabel and would make the two-state proof
// flaky.
type SetDoNotMergeReviewCommand struct {
	// URL is the GitHub PR URL; owner/repo/number are parsed from it.
	URL string `json:"url"`
	// HeadSHA is the commit the review is submitted against.
	HeadSHA string `json:"head_sha"`
	// Labels is the PR's label set at delivery time.
	Labels []string `json:"labels,omitempty"`
}

// Blocked reports whether the do-not-merge label is present.
func (cmd SetDoNotMergeReviewCommand) Blocked() bool {
	return hasDoNotMergeLabel(cmd.Labels)
}

// Validate enforces the command's schema rules. URL must parse to a
// GitHub-platform PR URL (same rules as TriggerPRReviewCommand), and HeadSHA
// must be non-empty — a review submitted against an empty commit is a 422 at
// the API boundary, far from the cause.
func (cmd SetDoNotMergeReviewCommand) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("URL", validation.HasValidationFunc(func(ctx context.Context) error {
			if cmd.URL == "" {
				return errors.Wrap(ctx, validation.Error, "url must not be empty")
			}
			prInfo, err := prurl.ParsePRURL(ctx, cmd.URL)
			if err != nil {
				return errors.Wrapf(ctx, err, "parse url %q", cmd.URL)
			}
			if prInfo.Platform != prurl.PlatformGitHub {
				return errors.Errorf(
					ctx,
					"only github platform is supported, got %s",
					prInfo.Platform,
				)
			}
			return nil
		})),
		validation.Name("HeadSHA", validation.HasValidationFunc(func(ctx context.Context) error {
			if cmd.HeadSHA == "" {
				return errors.Wrap(ctx, validation.Error, "head_sha must not be empty")
			}
			return nil
		})),
	}.Validate(ctx)
}
