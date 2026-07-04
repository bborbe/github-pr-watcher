// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"github.com/bborbe/github-pr-watcher/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Filter", func() {
	Describe("ParseBotAllowlist", func() {
		It("returns nil for empty string", func() {
			Expect(pkg.ParseBotAllowlist("")).To(BeNil())
		})

		It("returns single entry", func() {
			Expect(pkg.ParseBotAllowlist("dependabot[bot]")).To(Equal([]string{"dependabot[bot]"}))
		})

		It("returns multiple comma-separated entries", func() {
			Expect(
				pkg.ParseBotAllowlist("dependabot[bot],renovate[bot]"),
			).To(Equal([]string{"dependabot[bot]", "renovate[bot]"}))
		})

		It("trims leading and trailing whitespace from entries", func() {
			Expect(
				pkg.ParseBotAllowlist(" dependabot[bot] , renovate[bot] "),
			).To(Equal([]string{"dependabot[bot]", "renovate[bot]"}))
		})

		It("filters out entries that are only whitespace after trimming", func() {
			Expect(
				pkg.ParseBotAllowlist("dependabot[bot],   ,renovate[bot]"),
			).To(Equal([]string{"dependabot[bot]", "renovate[bot]"}))
		})

		It("filters out trailing empty entry from trailing comma", func() {
			Expect(pkg.ParseBotAllowlist("dependabot[bot],")).To(Equal([]string{"dependabot[bot]"}))
		})
	})

	Describe("ParseTrustedAuthors", func() {
		It("returns nil for empty string", func() {
			Expect(pkg.ParseTrustedAuthors("")).To(BeNil())
		})

		It("returns single entry", func() {
			Expect(pkg.ParseTrustedAuthors("alice")).To(Equal([]string{"alice"}))
		})

		It("returns multiple comma-separated entries trimmed", func() {
			Expect(
				pkg.ParseTrustedAuthors("alice , bob"),
			).To(Equal([]string{"alice", "bob"}))
		})

		It("filters out whitespace-only entries", func() {
			Expect(
				pkg.ParseTrustedAuthors("alice,  ,bob"),
			).To(Equal([]string{"alice", "bob"}))
		})

		It("filters out trailing empty entry from trailing comma", func() {
			Expect(pkg.ParseTrustedAuthors("alice,")).To(Equal([]string{"alice"}))
		})
	})
})
