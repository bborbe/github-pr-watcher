// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("matchesReviewerLogin", func() {
	DescribeTable("normalises both sides before comparing",
		func(reviewLogin string, logins []string, want bool) {
			Expect(matchesReviewerLogin(reviewLogin, logins)).To(Equal(want))
		},
		Entry("exact match", "octopus-pr-reviewer",
			[]string{"octopus-pr-reviewer"}, true),
		Entry("mixed-case review login", "OCTOPUS-PR-REVIEWER",
			[]string{"octopus-pr-reviewer"}, true),
		Entry("[bot] on the review side only", "octopus-pr-reviewer[bot]",
			[]string{"octopus-pr-reviewer"}, true),
		Entry("[BOT] uppercase suffix", "octopus-pr-reviewer[BOT]",
			[]string{"octopus-pr-reviewer"}, true),
		Entry("[bot] on the config side only", "octopus-pr-reviewer",
			[]string{"octopus-pr-reviewer[bot]"}, true),
		Entry("mixed case with suffix", "OCTOPUS-PR-REVIEWER[BOT]",
			[]string{"octopus-pr-reviewer"}, true),
		Entry("different login does not match", "octopus-pr-reviewer-dev",
			[]string{"octopus-pr-reviewer"}, false),
		Entry("empty review login never matches", "",
			[]string{"octopus-pr-reviewer"}, false),
		Entry("empty login list never matches", "octopus-pr-reviewer", nil, false),
		Entry("second entry matches", "octopus-pr-reviewer-dev",
			[]string{"octopus-pr-reviewer", "octopus-pr-reviewer-dev"}, true),
	)
})
