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

var _ = Describe("SetTaskLinkStatusCommand", func() {
	const (
		prURL    = "https://github.com/bborbe/repo/pull/42"
		taskUUID = "0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"
	)

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	DescribeTable("HasTaskLinkLine",
		func(body string, expected bool) {
			Expect(command.HasTaskLinkLine(body)).To(Equal(expected))
		},
		Entry("plain line", "Task: "+taskUUID, true),
		Entry("leading and trailing spaces", "   Task:   "+taskUUID+"   ", true),
		Entry("tabs", "\tTask:\t"+taskUUID+"\t", true),
		Entry("no space after colon", "Task:"+taskUUID, true),
		Entry("uppercase hex", "Task: 0B1C2D3E-4F50-6172-8394-A5B6C7D8E9F0", false),
		Entry("lowercase key", "task: "+taskUUID, false),
		Entry("subtask prefix", "subtask: "+taskUUID, false),
		Entry("not a task prefix", "not a task: "+taskUUID, false),
		Entry("markdown bullet", "- Task: "+taskUUID, false),
		Entry("trailing text after uuid", "Task: "+taskUUID+" extra", false),
		Entry("trailing char after uuid", "Task: "+taskUUID+"x", false),
		Entry(
			"CRLF body with match in the middle",
			"Summary line\r\n\r\nTask: "+taskUUID+"\r\nMore text\r\n",
			true,
		),
		Entry("LF body with match in the middle", "Summary\n\nTask: "+taskUUID+"\nfooter", true),
		Entry("key and uuid on different lines", "Task:\n"+taskUUID, false),
		Entry("empty body", "", false),
		Entry("no uuid", "Task: ", false),
	)

	Describe("Linked", func() {
		It("is true when the body holds a valid task line", func() {
			cmd := command.SetTaskLinkStatusCommand{
				URL:     prURL,
				HeadSHA: "abc123",
				Body:    "Summary\n\nTask: " + taskUUID + "\n",
			}
			Expect(cmd.Linked()).To(BeTrue())
		})

		It("is false when the body is empty", func() {
			cmd := command.SetTaskLinkStatusCommand{URL: prURL, HeadSHA: "abc123"}
			Expect(cmd.Linked()).To(BeFalse())
		})
	})

	Describe("Validate", func() {
		It("accepts a github PR URL with a head SHA", func() {
			cmd := command.SetTaskLinkStatusCommand{URL: prURL, HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("accepts an empty body", func() {
			cmd := command.SetTaskLinkStatusCommand{URL: prURL, HeadSHA: "abc123", Body: ""}
			Expect(cmd.Validate(ctx)).To(Succeed())
		})

		It("rejects an empty url", func() {
			cmd := command.SetTaskLinkStatusCommand{HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects an unparseable url", func() {
			cmd := command.SetTaskLinkStatusCommand{URL: "not-a-url", HeadSHA: "abc123"}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects a non-github url", func() {
			cmd := command.SetTaskLinkStatusCommand{
				URL:     "https://gitlab.com/bborbe/repo/-/merge_requests/42",
				HeadSHA: "abc123",
			}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})

		It("rejects an empty head SHA", func() {
			cmd := command.SetTaskLinkStatusCommand{URL: prURL}
			Expect(cmd.Validate(ctx)).NotTo(Succeed())
		})
	})

	Describe("operation", func() {
		It("has a valid operation name", func() {
			Expect(command.SetTaskLinkStatusCommandOperation.Validate(ctx)).To(Succeed())
		})
	})
})
