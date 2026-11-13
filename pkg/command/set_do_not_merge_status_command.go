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

// SetDoNotMergeStatusCommandOperation is the Kafka command operation for
// posting the `do-not-merge` commit status on a PR head SHA. Wire string:
// "set-do-not-merge-status".
//
// It rides the existing GithubPRReviewV1SchemaID topic trio: the in-pod
// command consumer dispatches by operation, so no new schema ID and no
// `bborbe/maintainer` change are needed.
const SetDoNotMergeStatusCommandOperation base.CommandOperation = "set-do-not-merge-status"

// DoNotMergeStatusContext is the commit-status context the master-protection
// ruleset requires in every repo that used to run a `check-do-not-merge`
// bridge workflow. Renaming it silently removes the merge gate.
const DoNotMergeStatusContext = "do-not-merge"

// DoNotMergeLabel is the PR label whose presence blocks the merge.
const DoNotMergeLabel = "do-not-merge"

// SetDoNotMergeStatusCommand is the payload for
// SetDoNotMergeStatusCommandOperation, published by the webhook handler on
// every pull_request delivery that can change the label state, and consumed
// by the in-pod command consumer.
//
// Labels is the PR's full label set as carried by the webhook payload. The
// executor derives the state from it rather than re-reading the PR: a second
// API read races a rapid label/unlabel and would make the two-state proof
// flaky.
type SetDoNotMergeStatusCommand struct {
	// URL is the GitHub PR URL; owner/repo/number are parsed from it.
	URL string `json:"url"`
	// HeadSHA is the commit the status is posted against.
	HeadSHA string `json:"head_sha"`
	// Labels is the PR's label set at delivery time.
	Labels []string `json:"labels,omitempty"`
}

// Blocked reports whether the do-not-merge label is present.
func (cmd SetDoNotMergeStatusCommand) Blocked() bool {
	return hasDoNotMergeLabel(cmd.Labels)
}

// hasDoNotMergeLabel is the label predicate both do-not-merge commands share.
// It is one function so the status route and the review route can never
// disagree about which label holds a PR.
func hasDoNotMergeLabel(labels []string) bool {
	for _, label := range labels {
		if label == DoNotMergeLabel {
			return true
		}
	}
	return false
}

// Validate enforces the command's schema rules. URL must parse to a
// GitHub-platform PR URL (same rules as TriggerPRReviewCommand), and HeadSHA
// must be non-empty — a status posted against an empty ref is a 422 at the
// API boundary, far from the cause.
func (cmd SetDoNotMergeStatusCommand) Validate(ctx context.Context) error {
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
