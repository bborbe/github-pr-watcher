// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"github.com/bborbe/github-pr-watcher/pkg"
	gogithub "github.com/google/go-github/v62/github"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func checkRun(status, conclusion string) *gogithub.CheckRun {
	return &gogithub.CheckRun{
		Status:     gogithub.String(status),
		Conclusion: gogithub.String(conclusion),
	}
}

var _ = Describe("MergeCommitStateForTest", func() {
	DescribeTable("folds combined status and check runs",
		func(combined string, runs []*gogithub.CheckRun, expected string) {
			Expect(pkg.MergeCommitStateForTest(combined, runs)).To(Equal(expected))
		},
		Entry("no statuses and no runs -> pending", "", nil, "pending"),
		Entry("combined success, no runs -> success", "success", nil, "success"),
		Entry("combined success, green run -> success", "success",
			[]*gogithub.CheckRun{checkRun("completed", "success")}, "success"),
		Entry("combined success, skipped run -> success", "success",
			[]*gogithub.CheckRun{checkRun("completed", "skipped")}, "success"),
		Entry("combined success, failing run -> failure (the check-run case)",
			"success", []*gogithub.CheckRun{checkRun("completed", "failure")}, "failure"),
		Entry("combined success, in-progress run -> pending", "success",
			[]*gogithub.CheckRun{checkRun("in_progress", "")}, "pending"),
		Entry("combined failure, green run -> failure", "failure",
			[]*gogithub.CheckRun{checkRun("completed", "success")}, "failure"),
		Entry("one failing run wins over a green sibling", "success",
			[]*gogithub.CheckRun{
				checkRun("completed", "success"),
				checkRun("completed", "timed_out"),
			}, "failure"),
	)
})
