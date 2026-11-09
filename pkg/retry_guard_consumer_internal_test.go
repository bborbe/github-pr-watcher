// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	agentlib "github.com/bborbe/agent"
	base "github.com/bborbe/cqrs/base"
	domain "github.com/bborbe/vault-cli/pkg/domain"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("retryGuardOutcomeTopic", func() {
	DescribeTable("derives the prefixed task event topic",
		func(prefix base.TopicPrefix, want string) {
			Expect(retryGuardOutcomeTopic(prefix).String()).To(Equal(want))
		},
		Entry("develop prefix", base.TopicPrefix("develop"), "develop-agent-task-v1-event"),
		Entry("empty prefix", base.TopicPrefix(""), "agent-task-v1-event"),
	)
})

var _ = Describe("retryGuardDiagnosticsOutcome", func() {
	DescribeTable("reads the outcome line from the Diagnostics section",
		func(content string, want string) {
			Expect(
				retryGuardDiagnosticsOutcome(context.Background(), agentlib.TaskContent(content)),
			).
				To(Equal(want))
		},
		Entry("fenced failed",
			"## Diagnostics\n\n```yaml\noutcome: failed\n```\n", "failed"),
		Entry("bare success",
			"## Diagnostics\n\noutcome: success\n", "success"),
		Entry("fenced skipped",
			"## Diagnostics\n\n```yaml\noutcome: skipped\n```\n", "skipped"),
		Entry("other section", "## Result\n\noutcome: failed\n", ""),
		Entry("empty content", "", ""),
	)
})

var _ = Describe("isRetryGuardTerminalStatus", func() {
	DescribeTable("classifies terminal statuses",
		func(status domain.TaskStatus, want bool) {
			Expect(isRetryGuardTerminalStatus(status)).To(Equal(want))
		},
		Entry("completed", domain.TaskStatusCompleted, true),
		Entry("aborted", domain.TaskStatusAborted, true),
		Entry("in_progress", domain.TaskStatusInProgress, false),
		Entry("next", domain.TaskStatusNext, false),
		Entry("backlog", domain.TaskStatusBacklog, false),
		Entry("hold", domain.TaskStatusHold, false),
	)
})
