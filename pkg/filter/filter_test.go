// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DraftFilter", func() {
	It("skips draft PRs", func() {
		f := filter.NewDraftFilter()
		Expect(f.Skip(filter.PR{IsDraft: true})).To(BeTrue())
	})
	It("does not skip non-draft PRs", func() {
		f := filter.NewDraftFilter()
		Expect(f.Skip(filter.PR{IsDraft: false})).To(BeFalse())
	})
})

var _ = Describe("BotAuthorFilter", func() {
	It("skips PR whose author matches an allowlist entry", func() {
		f := filter.NewBotAuthorFilter([]string{"dependabot[bot]", "renovate[bot]"})
		Expect(f.Skip(filter.PR{AuthorLogin: "dependabot[bot]"})).To(BeTrue())
		Expect(f.Skip(filter.PR{AuthorLogin: "renovate[bot]"})).To(BeTrue())
	})
	It("does not skip PR whose author is not in the allowlist", func() {
		f := filter.NewBotAuthorFilter([]string{"dependabot[bot]"})
		Expect(f.Skip(filter.PR{AuthorLogin: "alice"})).To(BeFalse())
	})
	It("never skips when allowlist is empty", func() {
		f := filter.NewBotAuthorFilter(nil)
		Expect(f.Skip(filter.PR{AuthorLogin: "alice"})).To(BeFalse())
		Expect(f.Skip(filter.PR{AuthorLogin: "dependabot[bot]"})).To(BeFalse())
	})
})

var _ = Describe("TaskCreationFilters composite", func() {
	It("returns false when slice is empty (vacuous — no filters configured)", func() {
		var fs filter.TaskCreationFilters
		Expect(fs.Skip(filter.PR{})).To(BeFalse())
	})
	It("returns true if any member votes skip", func() {
		fs := filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"alice"}),
		}
		Expect(fs.Skip(filter.PR{IsDraft: true})).To(BeTrue())
		Expect(fs.Skip(filter.PR{AuthorLogin: "alice"})).To(BeTrue())
	})
	It("returns false when no member votes skip", func() {
		fs := filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"alice"}),
		}
		Expect(fs.Skip(filter.PR{IsDraft: false, AuthorLogin: "bob"})).To(BeFalse())
	})
	It("supports the function adapter", func() {
		fs := filter.TaskCreationFilters{
			filter.TaskCreationFilterFunc(func(pr filter.PR) bool {
				return pr.AuthorLogin == "evil"
			}),
		}
		Expect(fs.Skip(filter.PR{AuthorLogin: "evil"})).To(BeTrue())
		Expect(fs.Skip(filter.PR{AuthorLogin: "alice"})).To(BeFalse())
	})
})
