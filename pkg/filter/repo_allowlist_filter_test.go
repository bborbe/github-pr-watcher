// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package filter_test

import (
	"context"

	"github.com/bborbe/github-pr-watcher/pkg/filter"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ParseRepoAllowlist", func() {
	var ctx context.Context
	BeforeEach(func() { ctx = context.Background() })

	It("returns nil for empty string (allow-all)", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeNil())
	})

	It("parses a single valid entry", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "github.com/bborbe/maintainer")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]string{"github.com/bborbe/maintainer"}))
	})

	It("parses multiple valid entries", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "github.com/bborbe/foo,github.com/bborbe/bar")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(ConsistOf("github.com/bborbe/foo", "github.com/bborbe/bar"))
	})

	It("strips whitespace around entries", func() {
		result, err := filter.ParseRepoAllowlist(
			ctx,
			" github.com/bborbe/foo , github.com/bborbe/bar ",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(ConsistOf("github.com/bborbe/foo", "github.com/bborbe/bar"))
	})

	It("silently drops empty entries from trailing comma", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "github.com/bborbe/foo,")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]string{"github.com/bborbe/foo"}))
	})

	It("silently drops whitespace-only entries", func() {
		result, err := filter.ParseRepoAllowlist(
			ctx,
			"github.com/bborbe/foo, ,github.com/bborbe/bar",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(ConsistOf("github.com/bborbe/foo", "github.com/bborbe/bar"))
	})

	It("returns nil for comma-only input (all entries empty after trim)", func() {
		result, err := filter.ParseRepoAllowlist(ctx, ",")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeNil())
	})

	It("accepts wildcard entry without error", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "github.com/bborbe/*")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]string{"github.com/bborbe/*"}))
	})

	It("accepts malformed two-segment entry without error", func() {
		result, err := filter.ParseRepoAllowlist(ctx, "bborbe/code-reviewer")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]string{"bborbe/code-reviewer"}))
	})
})

var _ = Describe("RepoAllowlistFilter", func() {
	It("never skips when allowlist is empty", func() {
		f := filter.NewRepoAllowlistFilter(nil)
		Expect(f.Skip(filter.PR{RepoKey: "github.com/bborbe/foo"})).To(BeFalse())
		Expect(f.Skip(filter.PR{RepoKey: ""})).To(BeFalse())
	})

	It("does not skip a PR whose RepoKey is on the allowlist", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/maintainer"})
		Expect(f.Skip(filter.PR{RepoKey: "github.com/bborbe/maintainer"})).To(BeFalse())
	})

	It("skips a PR whose RepoKey is NOT on the allowlist", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/maintainer"})
		Expect(f.Skip(filter.PR{RepoKey: "github.com/bborbe/other-repo"})).To(BeTrue())
	})

	It("skips a PR with an empty RepoKey when the allowlist is non-empty", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/maintainer"})
		Expect(f.Skip(filter.PR{RepoKey: ""})).To(BeTrue())
	})

	It("matches exactly — prefix match is not a match", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/code"})
		Expect(f.Skip(filter.PR{RepoKey: "github.com/bborbe/maintainer"})).To(BeTrue())
	})

	It("does not skip a PR whose RepoKey matches a wildcard allowlist entry", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"})
		Expect(f.Skip(filter.PR{RepoKey: "github.com/bborbe/maintainer"})).To(BeFalse())
	})

	It("skips a PR whose RepoKey owner does not match the wildcard entry", func() {
		f := filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"})
		Expect(f.Skip(filter.PR{RepoKey: "github.com/other-owner/repo"})).To(BeTrue())
	})
})
