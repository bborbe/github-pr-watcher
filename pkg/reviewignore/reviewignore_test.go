// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reviewignore_test

import (
	"github.com/bborbe/github-pr-watcher/pkg/reviewignore"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Parse", func() {
	var matcher reviewignore.Matcher

	DescribeTable("empty content excludes nothing",
		func(content string) {
			matcher = reviewignore.Parse([]byte(content))
			Expect(matcher.Match("main.go")).To(BeFalse())
			Expect(matcher.Match("vendor/foo/bar.go")).To(BeFalse())
		},
		Entry("empty", ""),
		Entry("blank lines", "\n\n\n"),
		Entry("comments only", "# nothing here\n# really\n"),
	)

	Context("directory patterns", func() {
		BeforeEach(func() {
			matcher = reviewignore.Parse([]byte("vendor/\nprompts/\nspecs/\n"))
		})

		DescribeTable("matches paths under an ignored directory",
			func(path string, want bool) {
				Expect(matcher.Match(path)).To(Equal(want))
			},
			Entry("vendored dep", "vendor/github.com/foo/bar.go", true),
			Entry("dark-factory prompt", "prompts/completed/151.md", true),
			Entry("dark-factory spec", "specs/in-progress/021.md", true),
			Entry("scenarios are NOT excluded", "scenarios/release.md", false),
			Entry("real source", "pkg/watcher.go", false),
			Entry("root file", "main.go", false),
		)
	})

	Context("glob semantics — not literal prefixes", func() {
		BeforeEach(func() {
			matcher = reviewignore.Parse([]byte("**/mocks/**\n*.snap\n"))
		})

		DescribeTable("depth wildcards and suffix globs",
			func(path string, want bool) {
				Expect(matcher.Match(path)).To(Equal(want))
			},
			Entry("mocks at root", "mocks/github_client.go", true),
			Entry("mocks nested one level", "pkg/mocks/watcher.go", true),
			Entry("mocks nested deep", "a/b/c/mocks/thing.go", true),
			Entry("snapshot at root", "fixture.snap", true),
			Entry("snapshot nested", "test/data/fixture.snap", true),
			Entry("not a snapshot", "test/data/fixture.json", false),
			Entry("mocks in the name but not a dir", "pkg/mockserver/main.go", false),
		)
	})

	Context("negation", func() {
		// git semantics, verified against
		// `git -c core.excludesFile=<file> check-ignore --no-index` on
		// 2026-09-08. These two cases differ, and the difference is the
		// gitignore spec's rule that a file cannot be re-included once a
		// parent directory is excluded.
		Context("under an excluded directory — negation does NOT re-include", func() {
			BeforeEach(func() {
				matcher = reviewignore.Parse([]byte("specs/\n!specs/README.md\n"))
			})

			It("excludes the directory contents", func() {
				Expect(matcher.Match("specs/021.md")).To(BeTrue())
			})

			It("keeps the negated path excluded, matching git", func() {
				// `sabhiram/go-gitignore` alone re-includes this; git does
				// not. The size gate must agree with the reviewer prompt,
				// which drives git's own engine — see parentExcluded.
				Expect(matcher.Match("specs/README.md")).To(BeTrue())
			})
		})

		Context("no excluded parent — negation DOES re-include", func() {
			BeforeEach(func() {
				matcher = reviewignore.Parse([]byte("*.snap\n!keep.snap\n"))
			})

			It("excludes the matched suffix", func() {
				Expect(matcher.Match("fixture.snap")).To(BeTrue())
			})

			It("re-includes the negated file", func() {
				Expect(matcher.Match("keep.snap")).To(BeFalse())
			})
		})
	})

	Context("self-exclusion guard", func() {
		DescribeTable("never excludes .reviewignore itself",
			func(content string) {
				matcher = reviewignore.Parse([]byte(content))
				Expect(matcher.Match(reviewignore.Filename)).To(BeFalse())
			},
			Entry("names itself directly", ".reviewignore\n"),
			Entry("bare wildcard", "*\n"),
			Entry("dotfile glob", ".*\n"),
			Entry("depth glob", "**/.reviewignore\n"),
			Entry("suffix glob", "*reviewignore\n"),
			Entry("negation ordering", "!foo\n.reviewignore\n"),
		)

		It("still excludes other paths when it names itself", func() {
			matcher = reviewignore.Parse([]byte(".reviewignore\nvendor/\n"))
			Expect(matcher.Match(reviewignore.Filename)).To(BeFalse())
			Expect(matcher.Match("vendor/foo.go")).To(BeTrue())
		})
	})

	Context("comments and blank lines among real patterns", func() {
		BeforeEach(func() {
			matcher = reviewignore.Parse(
				[]byte("# generated\nvendor/\n\n# pipeline state\nprompts/\n"),
			)
		})

		It("applies the real patterns", func() {
			Expect(matcher.Match("vendor/x.go")).To(BeTrue())
			Expect(matcher.Match("prompts/1.md")).To(BeTrue())
		})

		It("does not treat a comment as a pattern", func() {
			Expect(matcher.Match("generated")).To(BeFalse())
		})
	})
})
