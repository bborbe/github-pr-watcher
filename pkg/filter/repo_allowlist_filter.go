// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

import (
	"context"
	"strings"

	repoallowlist "github.com/bborbe/maintainer/lib/repoallowlist"
)

// ParseRepoAllowlist parses a comma-separated allowlist string into a slice
// of host-qualified repo keys. Whitespace is trimmed; empty entries are skipped.
// Returns (nil, nil) for empty input (allow-all).
// Entry well-formedness is NOT validated here — repoallowlist.IsAllowed handles
// malformed entries gracefully at match time (logs and skips).
func ParseRepoAllowlist(_ context.Context, raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var result []string
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			result = append(result, entry)
		}
	}
	return result, nil
}

// NewRepoAllowlistFilter returns a TaskCreationFilter that skips PRs whose
// RepoKey is not in the allowlist. An empty allowlist never skips (allow-all).
func NewRepoAllowlistFilter(allowlist []string) TaskCreationFilter {
	return &repoAllowlistFilter{allowlist: allowlist}
}

type repoAllowlistFilter struct {
	allowlist []string
}

func (f *repoAllowlistFilter) Skip(pr PR) bool {
	return !repoallowlist.IsAllowed(f.allowlist, pr.RepoKey)
}
