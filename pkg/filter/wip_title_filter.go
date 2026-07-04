// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

import "regexp"

// wipTitleRegexp matches titles whose first three characters are "WIP"
// followed by a colon or whitespace separator. Anchored at title start;
// intentionally NOT case-insensitive (the convention is uppercase).
//
// Matches: "WIP: foo", "WIP foo"
// Does NOT match: "wip: foo" (lowercase), "Working in progress" (mid-title),
//
//	"WIPER: foo" (no separator after WIP)
var wipTitleRegexp = regexp.MustCompile(`^WIP[:\s]`)

// NewWIPTitleFilter returns a filter that skips PRs whose title starts
// with "WIP:" or "WIP ". The author has explicitly signaled the PR is
// not ready for review.
func NewWIPTitleFilter() TaskCreationFilter {
	return &wipTitleFilter{}
}

type wipTitleFilter struct{}

func (f *wipTitleFilter) Skip(pr PR) bool {
	return wipTitleRegexp.MatchString(pr.Title)
}
