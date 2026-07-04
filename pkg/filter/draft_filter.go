// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter

// NewDraftFilter returns a filter that skips draft PRs (the author
// is signaling "not ready for review"). GitHub's draft state is the
// primary signal; literal "WIP" prefixes in the title are handled
// by a separate WIP filter (see wip_title_filter.go when added).
func NewDraftFilter() TaskCreationFilter {
	return &draftFilter{}
}

type draftFilter struct{}

func (f *draftFilter) Skip(pr PR) bool {
	return pr.IsDraft
}
