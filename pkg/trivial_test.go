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
		Entry(
			"go.sum hash pair bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n github.com/other/mod v1.0.0 h1:abc=\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum line with a go directive -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+go 1.26.6\n",
				},
			},
			false,
		),
		Entry(
			"go.sum bare module path -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+github.com/foo/bar\n",
				},
			},
			false,
		),
		Entry(
			"go.sum h2 hash line -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+github.com/foo/bar v1.2.4 h2:abc=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum comment -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,3 @@\n+# comment\n",
				},
			},
			false,
		),
		Entry(
			"go.sum unpaired removal -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,2 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum cross-module swap -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/baz v1.2.3 h1:def=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum go.mod suffix mismatch -> false",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3/go.mod h1:abc=\n+github.com/foo/bar v1.2.3 h1:def=\n",
				},
			},
			false,
		),
		Entry(
			"go.sum empty patch -> false",
			[]pkg.PRFile{{Filename: "go.sum", Patch: ""}},
			false,
		),
		Entry(
			"go.sum new module added -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,2 +1,4 @@\n+github.com/new/mod v1.0.0 h1:abc=\n+github.com/new/mod v1.0.0/go.mod h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum pseudo-version bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v0.0.0-20231231235959-abcdef123456 h1:abc=\n+github.com/foo/bar v0.0.0-20240101120000-abcdef123456 h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum +incompatible bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.9.0 h1:abc=\n+github.com/foo/bar v2.0.0+incompatible h1:def=\n",
				},
			},
			true,
		),
		Entry(
			"go.sum full dependency bump -> true",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo/bar v1.2.3\n+\tgithub.com/foo/bar v1.2.4\n",
				},
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
				{
					Filename: "CHANGELOG.md",
					Patch:    "--- a/CHANGELOG.md\n+++ b/CHANGELOG.md\n@@ -1,3 +1,4 @@\n ## Unreleased\n+\n+- chore: bump dependency\n",
				},
			},
			true,
		),
		Entry(
			"go.sum real boltkv bump -> true",
			// Origin: bborbe/boltkv#29 — the CHANGELOG.md, go.mod and go.sum
			// patches exactly as returned by
			// `gh -R bborbe/boltkv pr view 29 --json files`.
			[]pkg.PRFile{
				{
					Filename: "CHANGELOG.md",
					Patch: "@@ -8,6 +8,10 @@ Please choose versions by [Semantic Versioning](http://semver.org/).\n" +
						" * MINOR version when you add functionality in a backwards-compatible manner, and\n" +
						" * PATCH version when you make backwards-compatible bug fixes.\n" +
						" \n" +
						"+## Unreleased\n" +
						"+\n" +
						"+- chore: update go module dependencies\n" +
						"+\n" +
						" ## v1.16.3\n" +
						" \n" +
						" - chore: update Go to 1.27.3 and github.com/bborbe/errors to v1.6.4, github.com/bborbe/kv to v1.21.19, github.com/bborbe/sentry to v1.10.6, github.com/bborbe/service to v1.11.1, github.com/onsi/ginkgo/v2 to v2.33.1, github.com/onsi/gomega to v1.44.0",
				},
				{
					Filename: "go.mod",
					Patch: "@@ -16,7 +16,7 @@ require (\n" +
						" require (\n" +
						" \tgithub.com/Masterminds/semver/v3 v3.5.0 // indirect\n" +
						" \tgithub.com/bborbe/argument/v2 v2.13.8 // indirect\n" +
						"-\tgithub.com/bborbe/collection v1.21.3 // indirect\n" +
						"+\tgithub.com/bborbe/collection v1.21.4 // indirect\n" +
						" \tgithub.com/bborbe/math v1.4.16 // indirect\n" +
						" \tgithub.com/bborbe/parse v1.12.4 // indirect\n" +
						" \tgithub.com/bborbe/run v1.11.3 // indirect",
				},
				{
					Filename: "go.sum",
					Patch: "@@ -2,8 +2,8 @@ github.com/Masterminds/semver/v3 v3.5.0 h1:kQceYJfbupGfZOKZQg0kou0DgAKhzDg2NZPAw\n" +
						" github.com/Masterminds/semver/v3 v3.5.0/go.mod h1:4V+yj/TJE1HU9XfppCwVMZq3I84lprf4nC11bSS5beM=\n" +
						" github.com/bborbe/argument/v2 v2.13.8 h1:iAhOA67f3NMnOI0SwnaPT5+HW99vHA8whjORfDYmbtU=\n" +
						" github.com/bborbe/argument/v2 v2.13.8/go.mod h1:s2AypULcQok4aVLBTcJTFJ+0nemtVkrxwo2XFOgOSyo=\n" +
						"-github.com/bborbe/collection v1.21.3 h1:fAnKKYLGsBca/NNTuHtSgYIVad3Yw52Q9Krr/xCRO2k=\n" +
						"-github.com/bborbe/collection v1.21.3/go.mod h1:7N1XFVM4H41Ou6VaknELQHyEtfD5wly0lGz8LkqzJdU=\n" +
						"+github.com/bborbe/collection v1.21.4 h1:eyxNnsdbsxojlT9eDlGGclrW/vg8v+43/t0LVCUSZ8A=\n" +
						"+github.com/bborbe/collection v1.21.4/go.mod h1:k+qS5lxi4qQ/wpw8LiSBr08RiybIDc5/mbzs9yKDq0w=\n" +
						" github.com/bborbe/errors v1.6.4 h1:gPOl00mChbQ5WX9tqwA2zYRNjGFD32xBuyBV0Z47CD4=\n" +
						" github.com/bborbe/errors v1.6.4/go.mod h1:AjaF51Xl+xFEtNQyfSx6KkLR4qQkcM/6qi8xYaNjgEU=\n" +
						" github.com/bborbe/kv v1.21.19 h1:qgcm1QH4ATm4woOrKX6A007p5BVR4wm/KoFBMvoGvu8=",
				},
			},
			true,
		),
		Entry(
			"dependency bump plus vendor change -> false",
			[]pkg.PRFile{
				{
					Filename: "go.mod",
					Patch:    "--- a/go.mod\n+++ b/go.mod\n@@ -10,7 +10,7 @@\n-\tgithub.com/foo/bar v1.2.3\n+\tgithub.com/foo/bar v1.2.4\n",
				},
				{
					Filename: "go.sum",
					Patch:    "--- a/go.sum\n+++ b/go.sum\n@@ -1,3 +1,3 @@\n-github.com/foo/bar v1.2.3 h1:abc=\n+github.com/foo/bar v1.2.4 h1:def=\n",
				},
				{
					Filename: "vendor/modules.txt",
					Patch:    "--- a/vendor/modules.txt\n+++ b/vendor/modules.txt\n@@ -1,3 +1,3 @@\n-# github.com/foo/bar v1.2.3\n+# github.com/foo/bar v1.2.4\n",
				},
			},
			false,
		),
		Entry(
			"Casks version+sha256 bump -> true",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"   desc \"Web UI for Vault\"\n" +
						"   homepage \"https://github.com/bborbe/vault-ui\"\n" +
						"-  version \"0.97.0\"\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"   name \"vault-ui\"\n",
				},
			},
			true,
		),
		Entry(
			"Casks version only -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  version \"0.97.0\"\n" +
						"+  version \"0.98.0\"\n" +
						"   sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks sha256 only -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"   version \"0.98.0\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks url change -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,8 +3,8 @@\n" +
						"-  version \"0.97.0\"\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n" +
						"-  url \"https://example.com/vault-ui-0.97.0.tar.gz\"\n" +
						"+  url \"https://example.com/vault-ui-0.98.0.tar.gz\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks cross-kind pairing -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,6 @@\n" +
						"-  version \"0.97.0\"\n" +
						"+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks unpaired removal -> false",
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "--- a/Casks/vault-ui.rb\n+++ b/Casks/vault-ui.rb\n@@ -3,6 +3,5 @@\n" +
						"-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks empty patch -> false",
			[]pkg.PRFile{{Filename: "Casks/vault-ui.rb", Patch: ""}},
			false,
		),
		Entry(
			"Casks multi-arch block bump -> false",
			// Synthetic: every hash sits at the two-space indent, so the exact
			// count rule (not the line shape) is what rejects the four hashes.
			[]pkg.PRFile{
				{
					Filename: "Casks/dark-factory.rb",
					Patch: "--- a/Casks/dark-factory.rb\n+++ b/Casks/dark-factory.rb\n@@ -1,10 +1,10 @@\n" +
						"-  version \"1.2.3\"\n" +
						"+  version \"1.2.4\"\n" +
						"-  sha256 \"0000000000000000000000000000000000000000000000000000000000000000\"\n" +
						"+  sha256 \"1111111111111111111111111111111111111111111111111111111111111111\"\n" +
						"-  sha256 \"2222222222222222222222222222222222222222222222222222222222222222\"\n" +
						"+  sha256 \"3333333333333333333333333333333333333333333333333333333333333333\"\n" +
						"-  sha256 \"4444444444444444444444444444444444444444444444444444444444444444\"\n" +
						"+  sha256 \"5555555555555555555555555555555555555555555555555555555555555555\"\n" +
						"-  sha256 \"6666666666666666666666666666666666666666666666666666666666666666\"\n" +
						"+  sha256 \"7777777777777777777777777777777777777777777777777777777777777777\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks non-ruby file -> false",
			// The patch is a shape the .rb branch would accept, so only the
			// filename-suffix check can make this false — a patch the cask rule
			// rejects on its own would let this entry pass vacuously.
			[]pkg.PRFile{
				{
					Filename: "Casks/README.md",
					Patch:    "--- a/Casks/README.md\n+++ b/Casks/README.md\n@@ -1,3 +1,3 @@\n-  version \"0.97.0\"\n-  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n+  version \"0.98.0\"\n+  sha256 \"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\"\n",
				},
			},
			false,
		),
		Entry(
			"Casks real homebrew-tap bump -> true",
			// Origin: bborbe/homebrew-tap#29 — a vault-ui 0.98.0 bump, one file,
			// +2 -2. The patch below is the real hunk, pasted verbatim from
			// `gh -R bborbe/homebrew-tap pr diff 29`, including the real hashes,
			// the real hunk header, the comment context lines and the blank
			// context line before the url line.
			[]pkg.PRFile{
				{
					Filename: "Casks/vault-ui.rb",
					Patch: "@@ -3,8 +3,8 @@\n" +
						" # sha256 are bumped by .github/workflows/update-vault-ui-cask.yml when a new\n" +
						" # vault-ui release is published.\n" +
						" cask \"vault-ui\" do\n" +
						"-  version \"0.63.6\"\n" +
						"-  sha256 \"7435ca1d7bf4a461fb4732afe5c2e4d9ddfacdf611363cf0b8220faf7890885a\"\n" +
						"+  version \"0.98.0\"\n" +
						"+  sha256 \"2c7a3399648d5bbeb9559bfc3bf54efd72f8cc4ae84574ca3da7977a7281841b\"\n" +
						" \n" +
						"   url \"https://github.com/bborbe/vault-ui/releases/download/v#{version}/vault_ui-#{version}-py3-none-any.whl\"\n",
				},
			},
			true,
		),
		Entry(
			"Casks add-cask PR with workflow and readme -> false",
			// Origin: the file set of bborbe/homebrew-tap#3 — three files: the new
			// cask, the updater workflow, and the README. The patches below are
			// minimal reproductions of each file's shape, not that PR's verbatim
			// text; the file set is what the entry proves. The cask file is an
			// all-added file, so it is non-mechanical on its own; the two siblings
			// make the PR non-mechanical regardless.
			[]pkg.PRFile{
				{
					Filename: "Casks/git-ai-sync.rb",
					Patch: "@@ -0,0 +1,6 @@\n" +
						"+cask \"git-ai-sync\" do\n" +
						"+  version \"0.1.0\"\n" +
						"+  sha256 \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
						"+  url \"https://example.com/git-ai-sync-0.1.0.tar.gz\"\n" +
						"+  name \"git-ai-sync\"\n" +
						"+end\n",
				},
				{
					Filename: ".github/workflows/update-git-ai-sync-cask.yml",
					Patch: "@@ -0,0 +1,3 @@\n" +
						"+name: update cask\n" +
						"+on: workflow_dispatch\n" +
						"+jobs: {}\n",
				},
				{
					Filename: "README.md",
					Patch:    "--- a/README.md\n+++ b/README.md\n@@ -1,2 +1,3 @@\n # homebrew-tap\n+- git-ai-sync\n",
				},
			},
			false,
		),
	)
})
