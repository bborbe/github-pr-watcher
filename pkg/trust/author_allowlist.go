// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package trust

import (
	"context"
	"fmt"
)

// NewAuthorAllowlist returns a Trust leaf that grants trust by exact GitHub
// login membership (case-sensitive byte equality).
//
// Fail-safe rules:
//   - Empty members list → always denies.
//   - PR with empty AuthorLogin → always denies (author unknown).
//
// Operators are responsible for matching GitHub's stored casing.
func NewAuthorAllowlist(members []string) Trust {
	return Func(func(_ context.Context, pr PR) (Result, error) {
		if len(members) == 0 {
			return NewResult(false, "author-allowlist is empty (always denies)"), nil
		}
		if pr.AuthorLogin == "" {
			return NewResult(false, "author unknown (no login provided)"), nil
		}
		for _, m := range members {
			if m == pr.AuthorLogin {
				return NewResult(
					true,
					fmt.Sprintf("author %q in trusted-authors allowlist", pr.AuthorLogin),
				), nil
			}
		}
		return NewResult(
			false,
			fmt.Sprintf("author %q not in trusted-authors allowlist", pr.AuthorLogin),
		), nil
	})
}
