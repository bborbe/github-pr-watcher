// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TaskCreationFilter identities", func() {
	It("reports the exact Name() for every filter in the chain", func() {
		Expect(filter.NewDraftFilter().Name()).To(Equal("draft"))
		Expect(filter.NewBotAuthorFilter(nil).Name()).To(Equal("bot-author"))
		Expect(filter.NewWIPTitleFilter().Name()).To(Equal("wip-title"))
		Expect(filter.NewAgeFilter(0, libtime.DateTime{}).Name()).To(Equal("age"))
		Expect(filter.NewRepoAllowlistFilter(nil).Name()).To(Equal("repo-allowlist"))
	})
	It("reports \"inline\" for the function adapter", func() {
		Expect(filter.TaskCreationFilterFunc(func(filter.PR) bool { return false }).Name()).
			To(Equal("inline"))
	})
})

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
	It("SkippingFilter returns nil when the chain passes", func() {
		fs := filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"alice"}),
		}
		Expect(fs.SkippingFilter(filter.PR{AuthorLogin: "bob"})).To(BeNil())
	})
	It("SkippingFilter returns the FIRST voter when two filters both match", func() {
		// Both the draft filter and the WIP-title filter match this PR.
		// The chain must name the one that appears FIRST — that is the member
		// whose Skip() the short-circuit actually consulted, so it is the only
		// identity the skip log line can honestly report.
		draft := filter.NewDraftFilter()
		wip := filter.NewWIPTitleFilter()
		pr := filter.PR{IsDraft: true, Title: "WIP: both match"}

		first := filter.TaskCreationFilters{draft, wip}
		Expect(first.SkippingFilter(pr)).To(BeIdenticalTo(draft))
		Expect(first.SkippingFilter(pr).Name()).To(Equal("draft"))

		// Reversing the chain reverses the winner — proves the result is
		// positional, not an artifact of one filter's identity.
		second := filter.TaskCreationFilters{wip, draft}
		Expect(second.SkippingFilter(pr)).To(BeIdenticalTo(wip))
		Expect(second.SkippingFilter(pr).Name()).To(Equal("wip-title"))
	})
})
