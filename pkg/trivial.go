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
	// goSumLine matches one changed go.sum line: a module path, a version
	// (optionally carrying the `/go.mod` suffix), and an h1 hash.
	goSumLine = regexp.MustCompile(`^[+-]\S+ v\S+ h1:[A-Za-z0-9+/]+={0,2}$`)
	// caskVersionLine matches one changed line of a single-hash homebrew cask:
	// a `version "..."` line, indented by the two spaces real cask lines carry
	// inside the `cask` block.
	caskVersionLine = regexp.MustCompile(`^[+-]  version "[^"]*"$`)
	// caskSHA256Line matches one changed `sha256 "<64 lowercase hex>"` line of a
	// single-hash homebrew cask, at the same two-space indent.
	caskSHA256Line = regexp.MustCompile(`^[+-]  sha256 "[0-9a-f]{64}"$`)
)

// ClassifyTrivial returns true when every changed file in the PR is mechanical
// per the Mechanical Bump PR Pipeline allow-list: go.mod toolchain/version
// bumps, go.sum hash bumps, Dockerfile base-image/GO_VERSION bumps,
// append-only CHANGELOG, workflow go-version bumps, and
// single-hash homebrew cask version/sha256 bumps. Any out-of-pattern
// file (or an empty file list) returns false — conservative on edge cases: a
// false-positive auto-merge (silent behavior change shipped) costs more than a
// false-negative (waiting human merge).
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
	case f.Filename == "go.sum":
		return goSumMechanical(f.Patch)
	case f.Filename == "Dockerfile":
		return patchAll(f.Patch, dockerfileMechanical)
	case f.Filename == "CHANGELOG.md":
		return appendOnly(f.Patch)
	case strings.HasPrefix(f.Filename, ".github/workflows/") && strings.HasSuffix(f.Filename, ".yml"):
		return patchAll(f.Patch, workflowMechanical)
	case strings.HasPrefix(f.Filename, "Casks/") && strings.HasSuffix(f.Filename, ".rb"):
		return caskMechanical(f.Patch)
	default:
		return false
	}
}

// goSumMechanical returns true when a go.sum patch touches only module hash
// lines and every removed hash line is paired with an added hash line for the
// same module path and the same /go.mod suffix presence. An unpaired addition
// (a newly required module) is accepted; an unpaired removal is not, because
// it changes the build graph.
//
// A line whose exact (path, version, /go.mod) reappears on the other side is
// rejected: that is a hash rewritten at an unchanged version, not a bump. It
// replaces the pin go.sum exists to enforce, and it is the one go.sum edit a
// review would actually catch.
func goSumMechanical(patch string) bool {
	if patch == "" {
		return false
	}
	type moduleKey struct {
		path  string
		goMod bool
	}
	type moduleLine struct {
		path    string
		version string
		goMod   bool
	}
	removed := map[moduleKey]int{}
	added := map[moduleKey]int{}
	removedExact := map[moduleLine]struct{}{}
	addedExact := map[moduleLine]struct{}{}
	for _, line := range strings.Split(patch, "\n") {
		if line == "" ||
			strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		if !goSumLine.MatchString(line) {
			return false
		}
		fields := strings.Fields(line[1:])
		key := moduleKey{
			path:  fields[0],
			goMod: strings.HasSuffix(fields[1], "/go.mod"),
		}
		exact := moduleLine{
			path:    fields[0],
			version: strings.TrimSuffix(fields[1], "/go.mod"),
			goMod:   key.goMod,
		}
		if line[0] == '-' {
			removed[key]++
			removedExact[exact] = struct{}{}
		} else {
			added[key]++
			addedExact[exact] = struct{}{}
		}
	}
	for exact := range removedExact {
		if _, ok := addedExact[exact]; ok {
			return false
		}
	}
	for key, count := range removed {
		if added[key] < count {
			return false
		}
	}
	return true
}

// caskMechanical returns true when a Casks/<name>.rb patch touches only one
// version line and one sha256 line, each removed line paired with an added
// line of the same kind. Pairing is by kind, never by count: a version
// addition cannot pay for a sha256 removal, and a multi-architecture bump
// (several sha256 lines) is not mechanical.
func caskMechanical(patch string) bool {
	if patch == "" {
		return false
	}
	var removedVersion, addedVersion, removedSHA256, addedSHA256 int
	for _, line := range strings.Split(patch, "\n") {
		if line == "" ||
			strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "@@") {
			continue
		}
		if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
			continue
		}
		switch {
		case caskVersionLine.MatchString(line):
			if line[0] == '-' {
				removedVersion++
			} else {
				addedVersion++
			}
		case caskSHA256Line.MatchString(line):
			if line[0] == '-' {
				removedSHA256++
			} else {
				addedSHA256++
			}
		default:
			return false
		}
	}
	return removedVersion == 1 && addedVersion == 1 &&
		removedSHA256 == 1 && addedSHA256 == 1
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
