// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"
	"encoding/json"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	cqrsmocks "github.com/bborbe/cqrs/mocks"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	lib "github.com/bborbe/maintainer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TriggerPRReviewCommandOperation", func() {
	It("has expected string value", func() {
		Expect(command.TriggerPRReviewCommandOperation).
			To(Equal(base.CommandOperation("trigger-pr-review")))
	})

	It("passes cqrs operation regex validation", func() {
		// Boundary test: catches renames that violate the
		// `^[a-z][a-z-]*$` cqrs wire-string regex (e.g. underscores,
		// leading digit, uppercase). Per agent/lib precedent every
		// CommandOperation constant gets this check.
		Expect(command.TriggerPRReviewCommandOperation.Validate(context.Background())).
			To(Succeed())
	})
})

var _ = Describe("TriggerPRReviewCommand", func() {
	It("round-trips through JSON with both fields set", func() {
		cmd := command.TriggerPRReviewCommand{
			URL:   "https://github.com/bborbe/maintainer/pull/1",
			Force: true,
		}
		data, err := json.Marshal(cmd)
		Expect(err).To(BeNil())

		var got command.TriggerPRReviewCommand
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got.URL).To(Equal(cmd.URL))
		Expect(got.Force).To(Equal(cmd.Force))
	})

	It("omits force when zero (omitempty)", func() {
		cmd := command.TriggerPRReviewCommand{
			URL: "https://github.com/bborbe/maintainer/pull/1",
		}
		data, err := json.Marshal(cmd)
		Expect(err).To(BeNil())
		Expect(string(data)).NotTo(ContainSubstring("\"force\""))
	})

	It("JSON contains url and force keys when force is set", func() {
		cmd := command.TriggerPRReviewCommand{
			URL:   "https://github.com/bborbe/maintainer/pull/1",
			Force: true,
		}
		data, err := json.Marshal(cmd)
		Expect(err).To(BeNil())
		jsonStr := string(data)
		Expect(jsonStr).To(ContainSubstring(`"url"`))
		Expect(jsonStr).To(ContainSubstring(`"force"`))
	})

	It("JSON always contains url key", func() {
		cmd := command.TriggerPRReviewCommand{
			URL: "https://github.com/bborbe/maintainer/pull/1",
		}
		data, err := json.Marshal(cmd)
		Expect(err).To(BeNil())
		Expect(string(data)).To(ContainSubstring(`"url"`))
	})
})

var _ = Describe("TriggerPRReviewCommand.Validate", func() {
	DescribeTable("Validate",
		func(url string, expectError bool, errSubstring string) {
			cmd := command.TriggerPRReviewCommand{URL: url}
			err := cmd.Validate(context.Background())
			if expectError {
				Expect(err).To(HaveOccurred())
				if errSubstring != "" {
					Expect(err.Error()).To(ContainSubstring(errSubstring))
				}
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("valid github url",
			"https://github.com/bborbe/maintainer/pull/1", false, ""),
		Entry("empty url",
			"", true, "url must not be empty"),
		Entry("non-url string",
			"not-a-url", true, ""),
		Entry(
			"bitbucket platform",
			"https://bitbucket.example.com/projects/owner/repos/repo/pull-requests/1",
			true,
			"github platform",
		),
		Entry("ftp scheme",
			"ftp://github.com/owner/repo/pull/1", true, ""),
	)
})

var _ = Describe("NewTriggerPRReviewCommandSender", func() {
	var (
		ctx           context.Context
		cdbSender     *cqrsmocks.CDBCommandObjectSender
		triggerSender command.TriggerPRReviewCommandSender
	)

	BeforeEach(func() {
		ctx = context.Background()
		cdbSender = &cqrsmocks.CDBCommandObjectSender{}
		triggerSender = command.NewTriggerPRReviewCommandSender(
			newTestCommandCreator(10),
			cqrsiam.Initiator("test-watcher"),
			cdbSender,
		)
	})

	It("publishes one CommandObject keyed on the github-pr-review v1 schema", func() {
		cmd := command.TriggerPRReviewCommand{
			URL: "https://github.com/bborbe/maintainer/pull/42",
		}
		Expect(triggerSender.SendCommand(ctx, cmd)).To(Succeed())

		Expect(cdbSender.SendCommandObjectCallCount()).To(Equal(1))
		_, got := cdbSender.SendCommandObjectArgsForCall(0)
		Expect(got.Command.Operation).To(Equal(command.TriggerPRReviewCommandOperation))
		Expect(got.SchemaID).To(Equal(cdb.SchemaID(lib.GithubPRReviewV1SchemaID)))
	})

	It("does not call cdb sender when Validate fails", func() {
		// Empty URL fails Validate; sender must short-circuit.
		cmd := command.TriggerPRReviewCommand{URL: ""}
		err := triggerSender.SendCommand(ctx, cmd)
		Expect(err).To(HaveOccurred())
		Expect(cdbSender.SendCommandObjectCallCount()).To(Equal(0))
	})
})
