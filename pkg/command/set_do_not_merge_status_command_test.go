// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	"github.com/bborbe/github-pr-watcher/pkg/command"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SetDoNotMergeStatusCommand", func() {
	const prURL = "https://github.com/bborbe/repo/pull/42"

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Blocked", func() {
		It("is false with no labels", func() {
			cmd := command.SetDoNotMergeStatusCommand{URL: prURL}
			Expect(cmd.Blocked()).To(BeFalse())
		})

		It("is false with unrelated labels", func() {
			cmd := command.SetDoNotMergeStatusCommand{
				URL:    prURL,
				Labels: []string{"override-review", "size/large"},
			}
			Expect(cmd.Blocked()).To(BeFalse())
		})

		It("is true when the do-not-merge label is present", func() {
			cmd := command.SetDoNotMergeStatusCommand{
				URL:    prURL,
				Labels: []string{"override-review", "do-not-merge"},
			}
			Expect(cmd.Blocked()).To(BeTrue())
		})
	})

	Describe("Validate", func() {
		It("accepts a github PR URL with a head SHA", func() {
			cmd := command.SetDoNotMergeStatusCommand{URL: prURL, HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("rejects an empty url", func() {
			cmd := command.SetDoNotMergeStatusCommand{HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects an unparseable url", func() {
			cmd := command.SetDoNotMergeStatusCommand{URL: "not-a-url", HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects a non-github url", func() {
			cmd := command.SetDoNotMergeStatusCommand{
				URL:     "https://gitlab.com/bborbe/repo/-/merge_requests/42",
				HeadSHA: "abc123",
			}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects an empty head SHA", func() {
			cmd := command.SetDoNotMergeStatusCommand{URL: prURL}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})
	})
})
