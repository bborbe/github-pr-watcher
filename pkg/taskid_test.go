// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TaskID", func() {
	var prWatcherNamespace = uuid.MustParse("7d4b3e5f-8a21-4c9d-b036-2e5f7a8c1d0e")

	Describe("Derive", func() {
		It("is deterministic — same inputs always produce the same UUID", func() {
			a := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "abc123def456789a")
			b := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "abc123def456789a")
			Expect(a).To(Equal(b))
		})

		It("produces different UUIDs for different owner/repo/number combos", func() {
			a := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "abc123def456789a")
			b := pkg.DeriveTaskID("bborbe", "code-reviewer", 43, "abc123def456789a")
			c := pkg.DeriveTaskID("bborbe", "other-repo", 42, "abc123def456789a")
			d := pkg.DeriveTaskID("other-org", "code-reviewer", 42, "abc123def456789a")
			Expect(a).NotTo(Equal(b))
			Expect(a).NotTo(Equal(c))
			Expect(a).NotTo(Equal(d))
		})

		It(
			"produces the expected pinned UUID for bborbe/code-reviewer#42@abc123def456789a",
			func() {
				expected := uuid.NewSHA1(
					prWatcherNamespace,
					[]byte("bborbe/code-reviewer#42@abc123def456789a"),
				)
				Expect(
					pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "abc123def456789a"),
				).To(Equal(expected))
			},
		)

		It("produces different UUIDs for same PR but different SHAs", func() {
			a := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "sha-aaa")
			b := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "sha-bbb")
			Expect(a).NotTo(Equal(b))
		})

		It("two calls with identical (owner, repo, number, sha) produce the same UUID", func() {
			a := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "sha-stable")
			b := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "sha-stable")
			Expect(a).To(Equal(b))
		})
	})

	Describe("DeriveForce", func() {
		It(
			"produces a different UUID than DeriveTaskID for the same (owner, repo, number, sha)",
			func() {
				canonical := pkg.DeriveTaskID("bborbe", "code-reviewer", 42, "abc123def456789a")
				salted := pkg.DeriveTaskIDForce(
					"bborbe",
					"code-reviewer",
					42,
					"abc123def456789a",
					"nonce-1",
				)
				Expect(salted).NotTo(Equal(canonical))
			},
		)

		It("is stable for identical (owner, repo, number, sha, nonce) inputs", func() {
			a := pkg.DeriveTaskIDForce("bborbe", "code-reviewer", 42, "abc123def456789a", "nonce-x")
			b := pkg.DeriveTaskIDForce("bborbe", "code-reviewer", 42, "abc123def456789a", "nonce-x")
			Expect(a).To(Equal(b))
		})

		It(
			"produces different UUIDs for the same (owner, repo, number, sha) but different nonces",
			func() {
				a := pkg.DeriveTaskIDForce(
					"bborbe",
					"code-reviewer",
					42,
					"abc123def456789a",
					"nonce-a",
				)
				b := pkg.DeriveTaskIDForce(
					"bborbe",
					"code-reviewer",
					42,
					"abc123def456789a",
					"nonce-b",
				)
				Expect(a).NotTo(Equal(b))
			},
		)
	})
})
