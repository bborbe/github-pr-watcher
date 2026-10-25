// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package trust

import (
	"context"
	"fmt"
)

// NewSameRepo returns a Trust leaf that grants trust only when the pull
// request's head repository is the base repository it targets.
//
// An author login is not an ownership proof: a workflow running in a fork that
// opens a pull request against upstream renders as the same login (for example
// `github-actions[bot]`), so trusting that login alone would let a fork pull
// request be auto-reviewed. Comparing the head and base repository full names
// closes that hole.
//
// Fail-safe rules:
//   - Unknown head repo (empty HeadRepoFullName) → always denies.
//   - Unknown base repo (empty BaseRepoFullName) → always denies.
//   - Both empty never compares equal: the emptiness checks run first.
//
// The comparison is exact byte equality, matching NewAuthorAllowlist's handling
// of logins.
func NewSameRepo() Trust {
	return Func(func(_ context.Context, pr PR) (Result, error) {
		if pr.HeadRepoFullName == "" {
			return NewResult(false, "head repo unknown (no head repo full name provided)"), nil
		}
		if pr.BaseRepoFullName == "" {
			return NewResult(false, "base repo unknown (no base repo full name provided)"), nil
		}
		if pr.HeadRepoFullName == pr.BaseRepoFullName {
			return NewResult(
				true,
				fmt.Sprintf("head repo %q is the base repo", pr.HeadRepoFullName),
			), nil
		}
		return NewResult(
			false,
			fmt.Sprintf(
				"head repo %q is not the base repo %q",
				pr.HeadRepoFullName,
				pr.BaseRepoFullName,
			),
		), nil
	})
}
