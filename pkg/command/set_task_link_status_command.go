// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"
	"regexp"
	"strings"

	"github.com/bborbe/cqrs/base"
)

// SetTaskLinkStatusCommandOperation is the Kafka command operation for
// posting the `task-link` commit status on a PR head SHA. Wire string:
// "set-task-link-status".
//
// It rides the existing GithubPRReviewV1SchemaID topic trio: the in-pod
// command consumer dispatches by operation, so no new schema ID and no
// `bborbe/maintainer` change are needed.
const SetTaskLinkStatusCommandOperation base.CommandOperation = "set-task-link-status"

// TaskLinkStatusContext is the commit-status context the master-protection
// rulesets require by name. Renaming it silently breaks every merge in the
// repos that require it.
const TaskLinkStatusContext = "task-link"

// taskLinkLineRegexp is the predicate the retired per-repo `check-task-link`
// workflow applied with `printf '%s\n' "$PR_BODY" | grep -Eq '<ERE>'`. It must
// stay byte-equivalent to the ERE in `.github/workflows/check-task-link.yml`.
//
// Go's regexp is RE2 (linear time), so an untrusted PR body cannot trigger
// catastrophic backtracking.
var taskLinkLineRegexp = regexp.MustCompile(
	`^[[:space:]]*Task:[[:space:]]*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[[:space:]]*$`,
)

// SetTaskLinkStatusCommand is the payload for
// SetTaskLinkStatusCommandOperation, published by the webhook handler on every
// pull_request delivery that can change the body, and consumed by the in-pod
// command consumer.
//
// Body is the PR description at delivery time. The executor derives the state
// from it rather than re-reading the PR, for the same reason the do-not-merge
// command carries its label set: a second API read races a rapid edit.
type SetTaskLinkStatusCommand struct {
	// URL is the GitHub PR URL; owner/repo/number are parsed from it.
	URL string `json:"url"`
	// HeadSHA is the commit the status is posted against.
	HeadSHA string `json:"head_sha"`
	// Body is the PR description at delivery time. Empty is legitimate: a PR
	// with no description must produce a failure status.
	Body string `json:"body,omitempty"`
}

// Linked reports whether the PR body carries a line naming a task identifier.
func (cmd SetTaskLinkStatusCommand) Linked() bool {
	return HasTaskLinkLine(cmd.Body)
}

// HasTaskLinkLine reports whether any line of body is exactly a `Task:` line
// followed by a lowercase task UUID.
//
// It matches per line, never against the whole body with a multiline flag:
// [[:space:]] also matches a newline, so a whole-body match would let `Task:`
// and the UUID sit on different lines, which the line-based grep rejects.
func HasTaskLinkLine(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if taskLinkLineRegexp.MatchString(strings.TrimSuffix(line, "\r")) {
			return true
		}
	}
	return false
}

// Validate enforces the command's schema rules. URL must parse to a
// GitHub-platform PR URL (same rules as TriggerPRReviewCommand), and HeadSHA
// must be non-empty — a status posted against an empty ref is a 422 at the
// API boundary, far from the cause.
//
// Body is deliberately not validated: an empty body is a legitimate input
// that must produce a failure status, so rejecting it would leave a
// previously green PR green after its description was cleared.
func (cmd SetTaskLinkStatusCommand) Validate(ctx context.Context) error {
	return validatePRURLAndHeadSHA(ctx, cmd.URL, cmd.HeadSHA)
}
