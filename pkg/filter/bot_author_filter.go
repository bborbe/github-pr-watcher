// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewBotAuthorFilter returns a filter that skips PRs whose author
// matches any entry in the configured allowlist (exact match).
// An empty allowlist never skips.
func NewBotAuthorFilter(allowlist []string) TaskCreationFilter {
	return &botAuthorFilter{allowlist: allowlist}
}

type botAuthorFilter struct {
	allowlist []string
}

func (f *botAuthorFilter) Skip(pr PR) bool {
	for _, entry := range f.allowlist {
		if pr.AuthorLogin == entry {
			return true
		}
	}
	return false
}
