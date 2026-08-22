// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"github.com/bborbe/github-pr-watcher/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClassifyTrivial", func() {
	DescribeTable("mechanical allow-list verdict",
		func(files []pkg.PRFile, expected bool) {
			Expect(pkg.ClassifyTrivial(files)).To(Equal(expected))
		},
		Entry("empty file list -> false", nil, false),
		Entry(
			"go.mod toolchain bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -1,3 +1,3 @@\n-\tgo 1.26.5\n+\tgo 1.26.6\n",
				},
			},
			true,
		),
		Entry(
			"go.mod module version bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo/bar v1.2.3\n+\tgithub.com/foo/bar v1.2.4\n",
				},
			},
			true,
		),
		Entry(
			"go.mod with a non-mechanical line -> false",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo v1.2.3\n+\tgithub.com/foo v1.2.4\n+\treplace github.com/foo => ../foo\n",
				},
			},
			false,
		),
		Entry(
			"Dockerfile FROM golang bump -> true",
			[]pkg.PRFile{
				{
					Filename: "Dockerfile",
					Patch:    "--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,3 +1,3 @@\n-FROM golang:1.26.5\n+FROM golang:1.26.6\n",
				},
			},
			true,
		),
		Entry(
			"Dockerfile ARG GO_VERSION bump -> true",
			[]pkg.PRFile{
				{
					Filename: "Dockerfile",
					Patch:    "--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,3 +1,3 @@\n-ARG GO_VERSION=1.26.5\n+ARG GO_VERSION=1.26.6\n",
				},
			},
			true,
		),
		Entry(
			"Dockerfile with a non-mechanical line -> false",
			[]pkg.PRFile{
				{
					Filename: "Dockerfile",
					Patch:    "--- a/Dockerfile\n+++ b/Dockerfile\n@@ -1,3 +1,3 @@\n-FROM golang:1.26.5\n+FROM golang:1.26.6\n+RUN apt-get install foo\n",
				},
			},
			false,
		),
		Entry(
			"CHANGELOG append-only -> true",
			[]pkg.PRFile{
				{
					Filename: "CHANGELOG.md",
					Patch:    "--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,3 +1,4 @@\n ## Unreleased\n+\n+- feat: bump dependency\n",
				},
			},
			true,
		),
		Entry(
			"CHANGELOG with a removal -> false",
			[]pkg.PRFile{
				{
					Filename: "CHANGELOG.md",
					Patch:    "--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,3 +1,3 @@\n ## Unreleased\n-\n-- feat: removed bullet\n+## v0.1.0\n",
				},
			},
			false,
		),
		Entry(
			"workflow go-version bump -> true",
			[]pkg.PRFile{
				{
					Filename: ".github/workflows/ci.yml",
					Patch:    "--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -10,7 +10,7 @@\n-  go-version: 1.26.5\n+  go-version: 1.26.6\n",
				},
			},
			true,
		),
		Entry(
			"workflow with a non-mechanical line -> false",
			[]pkg.PRFile{
				{
					Filename: ".github/workflows/ci.yml",
					Patch:    "--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -10,7 +10,7 @@\n-  go-version: 1.26.5\n+  go-version: 1.26.6\n+  run: echo hello\n",
				},
			},
			false,
		),
		Entry(
			"source file change -> false",
			[]pkg.PRFile{
				{
					Filename: "pkg/watcher.go",
					Patch:    "--- a/pkg/watcher.go\n+++ b/pkg/watcher.go\n@@ -1,3 +1,3 @@\n-package pkg\n+package pkg2\n",
				},
			},
			false,
		),
		Entry("mixed mechanical + source file -> false",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -1,3 +1,3 @@\n-\tgo 1.26.5\n+\tgo 1.26.6\n",
				},
				{
					Filename: "pkg/watcher.go",
					Patch:    "--- a/pkg/watcher.go\n+++ b/pkg/watcher.go\n@@ -1,3 +1,3 @@\n-package pkg\n+package pkg2\n",
				},
			},
			false),
		Entry("empty patch (large/binary) -> false",
			[]pkg.PRFile{{Filename: "go.mod", Patch: ""}},
			false),
	)
})
