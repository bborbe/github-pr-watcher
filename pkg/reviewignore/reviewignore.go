// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package reviewignore parses a repo's root `.reviewignore` file — gitignore
// syntax — into a matcher the size gate uses to exclude non-reviewable paths
// (vendored deps, generated mocks, dark-factory pipeline state) from the
// added-line and changed-file counts that decide whether a PR parks at
// human_review.
//
// The file is by construction a way to hide code from review, so visibility is
// the control rather than restriction: callers report how much was excluded,
// and the file can never exclude itself (see Parse).
package reviewignore

import (
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// Filename is the repo-root path of the ignore file. It is also the one path
// no pattern may ever match — see Parse.
const Filename = ".reviewignore"

//counterfeiter:generate -o ../../mocks/review_ignore_matcher.go --fake-name ReviewIgnoreMatcher . Matcher

// Matcher reports whether a repo-relative path is excluded from review.
type Matcher interface {
	// Match reports whether path is excluded. path is repo-relative and
	// slash-separated, matching the GitHub API's changed-file name form.
	Match(path string) bool

	// Empty reports whether the matcher holds no patterns, so Match is false
	// for every path. Callers use this to skip work that only an actual
	// exclusion would justify — notably the per-file GitHub API call the
	// size gate would otherwise make for every repo, opted in or not.
	Empty() bool
}

// Parse is named for what it does to the input, not `NewMatcher` — it mirrors
// `maintainerconfig.Parse`, the sibling repo-root config parser this package
// was modeled on, and the stdlib `time.Parse`/`url.Parse` shape.
//
// Parse builds a Matcher from `.reviewignore` content. Empty, blank or
// all-comment content yields a matcher that excludes nothing — an absent or
// empty file is never an implicit "ignore everything".
//
// Parse enforces the self-exclusion guard: the returned Matcher reports false
// for Filename regardless of the patterns. The guard is applied after matching
// rather than by filtering patterns at parse time, because a pattern can reach
// `.reviewignore` through a `**` glob, a bare `*`, or negation ordering — the
// post-match override is total where pattern filtering would leak. The
// consequence is deliberate: `.reviewignore`'s own added lines always count
// toward the size gate and never appear in the reported excluded count, so a
// repo cannot use the file to conceal edits to the file itself.
func Parse(content []byte) Matcher {
	lines := strings.Split(string(content), "\n")
	patterns := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		patterns++
	}
	return &matcher{
		ignore: gitignore.CompileIgnoreLines(lines...),
		empty:  patterns == 0,
	}
}

type matcher struct {
	ignore *gitignore.GitIgnore
	empty  bool
}

func (m *matcher) Match(path string) bool {
	if path == Filename {
		return false
	}
	if m.ignore.MatchesPath(path) {
		return true
	}
	return m.parentExcluded(path)
}

// parentExcluded reports whether any ancestor directory of path is excluded.
//
// This exists because `sabhiram/go-gitignore` diverges from git here. The
// gitignore spec is explicit: "It is not possible to re-include a file if a
// parent directory of that file is excluded." Given
//
//	specs/
//	!specs/README.md
//
// git leaves `specs/README.md` ignored; the library re-includes it. Verified
// against `git -c core.excludesFile=... check-ignore` on 2026-09-08.
//
// The divergence matters because the size gate (this package) and the reviewer
// prompt (`/coding:pr-review`, which drives git's own ignore engine) must agree
// about the same file — otherwise a path counted as reviewable by the gate is
// withheld from the reviewer, or vice versa. Re-checking ancestors makes this
// package follow git.
func (m *matcher) parentExcluded(path string) bool {
	for i, r := range path {
		if r != '/' {
			continue
		}
		// Test both forms: the library matches directory patterns like
		// `specs/` against the trailing-slash spelling, and plain name
		// patterns like `specs` against the bare one.
		dir := path[:i]
		if m.ignore.MatchesPath(dir) || m.ignore.MatchesPath(dir+"/") {
			return true
		}
	}
	return false
}

func (m *matcher) Empty() bool {
	return m.empty
}
