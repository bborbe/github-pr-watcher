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

var _ = Describe("trust.NewAuthorAllowlist", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("always denies when members list is empty", func() {
		leaf := trust.NewAuthorAllowlist(nil)
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("empty"))
	})

	It("always denies when members list is empty slice", func() {
		leaf := trust.NewAuthorAllowlist([]string{})
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
	})

	It("trusts an author present in the list (exact match)", func() {
		leaf := trust.NewAuthorAllowlist([]string{"alice", "bob"})
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeTrue())
		Expect(r.Description()).To(ContainSubstring("alice"))
	})

	It("denies an author not in the list", func() {
		leaf := trust.NewAuthorAllowlist([]string{"alice", "bob"})
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: "carol"})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("carol"))
	})

	It("is case-sensitive: Alice does not match alice", func() {
		leaf := trust.NewAuthorAllowlist([]string{"alice"})
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: "Alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
	})

	It("denies when AuthorLogin is empty, with 'unknown' in description", func() {
		leaf := trust.NewAuthorAllowlist([]string{"alice"})
		r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: ""})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Success()).To(BeFalse())
		Expect(r.Description()).To(ContainSubstring("unknown"))
	})

	It("description is non-empty for all outcomes", func() {
		leaf := trust.NewAuthorAllowlist([]string{"alice"})
		for _, login := range []string{"alice", "stranger", ""} {
			r, err := leaf.IsTrusted(ctx, trust.PR{AuthorLogin: login})
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Description()).NotTo(BeEmpty())
		}
	})
})
