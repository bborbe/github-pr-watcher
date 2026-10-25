// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package trust_test

import (
	"context"

	"github.com/bborbe/github-pr-watcher/pkg/trust"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("trust.NewSameRepo", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("trusts a pull request whose head repo is the base repo", func() {
		leaf := trust.NewSameRepo()
		r, err := leaf.IsTrusted(ctx, trust.PR{
			HeadRepoFullName: "owner/repo",
			BaseRepoFullName: "owner/repo",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
		Expect(r.Description()).To(ContainSubstring("owner/repo"))
	})

	It("denies a fork pull request and names both repos", func() {
		leaf := trust.NewSameRepo()
		r, err := leaf.IsTrusted(ctx, trust.PR{
			HeadRepoFullName: "forkowner/repo",
			BaseRepoFullName: "owner/repo",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("forkowner/repo"))
		Expect(r.Description()).To(ContainSubstring("owner/repo"))
	})

	It("denies when the head repo is unknown", func() {
		leaf := trust.NewSameRepo()
		r, err := leaf.IsTrusted(ctx, trust.PR{
			HeadRepoFullName: "",
			BaseRepoFullName: "owner/repo",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("head"))
	})

	It("denies when the base repo is unknown", func() {
		leaf := trust.NewSameRepo()
		r, err := leaf.IsTrusted(ctx, trust.PR{
			HeadRepoFullName: "owner/repo",
			BaseRepoFullName: "",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("base"))
	})

	It("denies when both repos are unknown (never a vacuous match)", func() {
		leaf := trust.NewSameRepo()
		r, err := leaf.IsTrusted(ctx, trust.PR{})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
	})

	It("narrows the production chain: a fork PR from a trusted author is denied", func() {
		chain := trust.And{
			trust.NewAuthorAllowlist([]string{"alice"}),
			trust.NewSameRepo(),
		}

		sameRepo, err := chain.IsTrusted(ctx, trust.PR{
			AuthorLogin:      "alice",
			HeadRepoFullName: "owner/repo",
			BaseRepoFullName: "owner/repo",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(sameRepo.Success()).To(BeTrue())

		fork, err := chain.IsTrusted(ctx, trust.PR{
			AuthorLogin:      "alice",
			HeadRepoFullName: "forkowner/repo",
			BaseRepoFullName: "owner/repo",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(fork.Success()).To(BeFalse())
	})

	It("description is non-empty for all outcomes", func() {
		leaf := trust.NewSameRepo()
		for _, pr := range []trust.PR{
			{HeadRepoFullName: "owner/repo", BaseRepoFullName: "owner/repo"},
			{HeadRepoFullName: "forkowner/repo", BaseRepoFullName: "owner/repo"},
			{HeadRepoFullName: "", BaseRepoFullName: "owner/repo"},
			{HeadRepoFullName: "owner/repo", BaseRepoFullName: ""},
			{},
		} {
			r, err := leaf.IsTrusted(ctx, pr)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Description()).NotTo(BeEmpty())
		}
	})
})
