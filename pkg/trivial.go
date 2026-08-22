// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

var (
	// goModMechanical matches a go.mod patch line that is a toolchain bump
	// (`+\tgo 1.26.6`) or a module version bump (`+\tgithub.com/foo v1.2.4`).
	goModMechanical = regexp.MustCompile(
		`^[+-]\s*(go\s+[0-9]+\.[0-9]+(\.[0-9]+)?|[a-zA-Z0-9_.-]+(\/[a-zA-Z0-9_.-]+)*\s+v[0-9]+\.[0-9]+(\.[0-9]+)?)`,
	)
	// dockerfileMechanical matches a Dockerfile patch line that is a base-image
	// bump (`+FROM golang:1.26.6`) or a GO_VERSION arg bump
	// (`+ARG GO_VERSION=1.26.6`).
	dockerfileMechanical = regexp.MustCompile(
		`^[+-]\s*(FROM golang:[0-9]+\.[0-9]+(\.[0-9]+)?|ARG GO_VERSION=[0-9]+\.[0-9]+(\.[0-9]+)?)`,
	)
	// workflowMechanical matches a workflow patch line that is a go-version
	// value bump (`+  go-version: 1.26.6`).
	workflowMechanical = regexp.MustCompile(`^[+-]\s*go-version:\s*[0-9]+\.[0-9]+(\.[0-9]+)?`)
)

// ClassifyTrivial returns true when every changed file in the PR is mechanical
// per the Mechanical Bump PR Pipeline allow-list: go.mod toolchain/version
// bumps, Dockerfile base-image/GO_VERSION bumps, append-only CHANGELOG, and
// workflow go-version bumps. Any out-of-pattern file (or an empty file list)
// returns false — conservative on edge cases: a false-positive auto-merge
// (silent behavior change shipped) costs more than a false-negative (waiting
// human merge).
func ClassifyTrivial(files []PRFile) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !isMechanicalFile(f) {
			return false
		}
	}
	return true
}

// isMechanicalFile applies the per-filename allow-list to one changed file.
// Files outside the allow-list are never trivial.
func isMechanicalFile(f PRFile) bool {
	switch {
	case f.Filename == "go.mod":
		return patchAll(f.Patch, goModMechanical)
	case f.Filename == "Dockerfile":
		return patchAll(f.Patch, dockerfileMechanical)
	case f.Filename == "CHANGELOG.md":
		return appendOnly(f.Patch)
	case strings.HasPrefix(f.Filename, ".github/workflows/") && strings.HasSuffix(f.Filename, ".yml"):
		return patchAll(f.Patch, workflowMechanical)
	default:
		return false
	}
}

// patchAll returns true when every added/removed line in a unified-diff patch
// matches the pattern. Diff scaffolding (--- / +++ file headers, @@ hunk
// headers, and space-prefixed context lines) is ignored — only real +/- change
// lines are validated. An empty patch (large or binary file) returns false: we
// cannot prove it is mechanical.
func patchAll(patch string, re *regexp.Regexp) bool {
	if patch == "" {
		return false
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			if !re.MatchString(line) {
				return false
			}
		}
	}
	return true
}

// appendOnly returns true when the patch adds lines but removes none — the
// CHANGELOG append-only contract. File headers (`---` / `+++`) are skipped.
func appendOnly(patch string) bool {
	if patch == "" {
		return false
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "-") {
			return false
		}
	}
	return true
}
