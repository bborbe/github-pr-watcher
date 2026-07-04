// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package filter implements the TaskCreationFilter chain — predicates
// that decide whether a vault task should be created for a PR at all.
//
// This is one of two decision chains in the watcher:
//
//  1. TaskCreationFilter (this package): "should we create a task?"
//  2. trust.Trust (sibling package):     "given we create a task,
//     should it auto-process or
//     route to human_review?"
//
// See docs/watcher-decision-chains.md for the full split rationale.
package filter

import libtime "github.com/bborbe/time"

// PR is the filter-evaluation input derived from a GitHub pull request.
// Only the fields needed for task-creation filter decisions are included.
type PR struct {
	AuthorLogin string
	IsDraft     bool
	Title       string
	UpdatedAt   libtime.DateTime
	RepoKey     string
}

//counterfeiter:generate -o ../../mocks/task_creation_filter.go --fake-name TaskCreationFilter . TaskCreationFilter

// TaskCreationFilter decides whether a single PR should be skipped
// (no vault task created). Implementations return true to skip.
type TaskCreationFilter interface {
	// Skip returns true if the PR should be excluded from task creation.
	Skip(pr PR) bool
}

// TaskCreationFilterFunc adapts a function to the TaskCreationFilter
// interface (function-as-implementation, useful for inline filters).
type TaskCreationFilterFunc func(pr PR) bool

// Skip implements TaskCreationFilter for the function adapter.
func (f TaskCreationFilterFunc) Skip(pr PR) bool {
	return f(pr)
}

// TaskCreationFilters is a slice composite: skip if ANY member votes skip.
// An empty slice never skips (no filters configured = process every PR).
type TaskCreationFilters []TaskCreationFilter

// Skip returns true if any contained filter votes skip. Iteration is
// short-circuit on first hit (filters are pure predicates with no
// side effects, so there is no audit-trail concern).
func (fs TaskCreationFilters) Skip(pr PR) bool {
	for _, f := range fs {
		if f.Skip(pr) {
			return true
		}
	}
	return false
}
