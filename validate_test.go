// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("resolveAuth", func() {
	ctx := context.Background()

	It("returns error when App credentials are not configured", func() {
		// Ensure App credentials are unset
		GinkgoT().Setenv("APP_ID", "")
		GinkgoT().Setenv("INSTALLATION_ID", "")
		GinkgoT().Setenv("PEM_KEY", "")

		app := &application{}
		client, err := app.resolveAuth(ctx)
		Expect(err).To(HaveOccurred())
		Expect(client).To(BeNil())
		Expect(err.Error()).To(ContainSubstring("APP_ID"))
		Expect(err.Error()).NotTo(ContainSubstring("GH_TOKEN"))
	})

	It("returns partial-config error when only APP_ID is set", func() {
		GinkgoT().Setenv("APP_ID", "123")
		GinkgoT().Setenv("INSTALLATION_ID", "")
		GinkgoT().Setenv("PEM_KEY", "")

		app := &application{}
		client, err := app.resolveAuth(ctx)
		Expect(err).To(HaveOccurred())
		Expect(client).To(BeNil())
		Expect(err.Error()).To(ContainSubstring("partial GitHub App config"))
		Expect(err.Error()).To(ContainSubstring("INSTALLATION_ID"))
		Expect(err.Error()).To(ContainSubstring("PEM_KEY"))
		Expect(err.Error()).NotTo(ContainSubstring("GH_TOKEN"))
	})

	It(
		"returns partial-config error when APP_ID + INSTALLATION_ID are set but PEM_KEY is missing",
		func() {
			GinkgoT().Setenv("APP_ID", "123")
			GinkgoT().Setenv("INSTALLATION_ID", "456")
			GinkgoT().Setenv("PEM_KEY", "")

			app := &application{}
			client, err := app.resolveAuth(ctx)
			Expect(err).To(HaveOccurred())
			Expect(client).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("partial GitHub App config"))
			Expect(err.Error()).To(ContainSubstring("PEM_KEY"))
			Expect(err.Error()).NotTo(ContainSubstring("GH_TOKEN"))
		},
	)
})

var _ = DescribeTable("parseMaxPRAge",
	func(raw string, expected libtime.Duration, expectError bool, errContains string) {
		ctx := context.Background()
		got, err := parseMaxPRAge(ctx, raw)
		if expectError {
			Expect(err).To(HaveOccurred())
			if errContains != "" {
				Expect(err.Error()).To(ContainSubstring(errContains))
			}
		} else {
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		}
	},
	Entry("empty string disables age filter", "", libtime.Duration(0), false, ""),
	Entry("2160h parses correctly", "2160h", libtime.Duration(2160*time.Hour), false, ""),
	Entry("90d equals 2160h", "90d", libtime.Duration(2160*time.Hour), false, ""),
	Entry("negative duration is rejected", "-1h", libtime.Duration(0), true, "negative"),
	Entry("garbage input returns parse error", "not-a-duration", libtime.Duration(0), true, ""),
)

var _ = DescribeTable("parseBackfillDuration",
	func(raw string, expected libtime.Duration, expectError bool, errContains string) {
		ctx := context.Background()
		got, err := parseBackfillDuration(ctx, raw)
		if expectError {
			Expect(err).To(HaveOccurred())
			if errContains != "" {
				Expect(err.Error()).To(ContainSubstring(errContains))
			}
		} else {
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(expected))
		}
	},
	Entry("empty string disables backfill", "", libtime.Duration(0), false, ""),
	Entry("720h parses correctly", "720h", libtime.Duration(720*time.Hour), false, ""),
	Entry("30d equals 720h", "30d", libtime.Duration(720*time.Hour), false, ""),
	Entry("negative duration is rejected", "-1h", libtime.Duration(0), true, "negative"),
	Entry("garbage input returns parse error", "not-a-duration", libtime.Duration(0), true, ""),
)

var _ = DescribeTable(
	"validateLengthCaps",
	func(maxSlugLen, maxTitleLen int, expectError bool, errContains string) {
		ctx := context.Background()
		err := validateLengthCaps(ctx, maxSlugLen, maxTitleLen)
		if expectError {
			Expect(err).To(HaveOccurred())
			if errContains != "" {
				Expect(err.Error()).To(ContainSubstring(errContains))
			}
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	},
	Entry("valid defaults", 80, 200, false, ""),
	Entry("custom valid values", 30, 100, false, ""),
	Entry("MaxSlugLen=0 is rejected", 0, 200, true, "MAX_SLUG_LEN must be > 0"),
	Entry("MaxSlugLen=-5 is rejected", -5, 200, true, "MAX_SLUG_LEN must be > 0"),
	Entry("MaxTitleLen=0 is rejected", 80, 0, true, "MAX_TITLE_LEN must be > 0"),
	Entry("MaxTitleLen=-1 is rejected", 80, -1, true, "MAX_TITLE_LEN must be > 0"),
	Entry("MaxSlugLen equal to MaxTitleLen is rejected", 200, 200, true, "must be < MAX_TITLE_LEN"),
	Entry(
		"MaxSlugLen greater than MaxTitleLen is rejected",
		300,
		200,
		true,
		"must be < MAX_TITLE_LEN",
	),
)

var _ = DescribeTable("validateRepoScope",
	func(scope string, expectError bool) {
		ctx := context.Background()
		err := validateRepoScope(ctx, scope)
		if expectError {
			Expect(err).To(HaveOccurred())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	},
	Entry("simple username", "bborbe", false),
	Entry("org with hyphen", "my-org", false),
	Entry("org with dot", "org.name", false),
	Entry("org with underscore", "org_name", false),
	Entry("mixed case and digits", "Org123", false),
	Entry("space injection", "user is:issue", true),
	Entry("semicolon injection", "user;drop", true),
	Entry("empty string", "", true),
	Entry("plus injection", "user+more", true),
)
