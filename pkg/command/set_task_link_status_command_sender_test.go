// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	cdb "github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	cdbmocks "github.com/bborbe/cqrs/mocks"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	lib "github.com/bborbe/maintainer"
)

var _ = Describe("SetTaskLinkStatusCommandSender.SendCommand", func() {
	const taskUUID = "0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"

	var (
		ctx      context.Context
		fakeCDB  *cdbmocks.CDBCommandObjectSender
		sender   command.SetTaskLinkStatusCommandSender
		validCmd command.SetTaskLinkStatusCommand
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeCDB = new(cdbmocks.CDBCommandObjectSender)
		sender = command.NewSetTaskLinkStatusCommandSender(
			newTestCommandCreator(10),
			cqrsiam.Initiator("test-watcher"),
			fakeCDB,
		)
		validCmd = command.SetTaskLinkStatusCommand{
			URL:     "https://github.com/bborbe/repo/pull/42",
			HeadSHA: "abc123",
			Body:    "Summary\n\nTask: " + taskUUID + "\n",
		}
	})

	Context("valid command", func() {
		It("publishes one CommandObject with the correct operation and SchemaID", func() {
			fakeCDB.SendCommandObjectReturns(nil)

			Expect(sender.SendCommand(ctx, validCmd)).To(Succeed())

			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(1))
			_, obj := fakeCDB.SendCommandObjectArgsForCall(0)
			Expect(obj.SchemaID).To(Equal(lib.GithubPRReviewV1SchemaID))
			Expect(obj.Command.Operation).To(Equal(command.SetTaskLinkStatusCommandOperation))
		})
	})

	Context("validation fails", func() {
		It("returns a wrapped validation error and does NOT touch Kafka", func() {
			invalid := command.SetTaskLinkStatusCommand{URL: "", HeadSHA: "abc123"}

			err := sender.SendCommand(ctx, invalid)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("validate SetTaskLinkStatusCommand"))
			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(0))
		})
	})

	Context("Kafka publish fails", func() {
		It("returns a wrapped Kafka error", func() {
			fakeCDB.SendCommandObjectReturns(errors.Errorf(ctx, "broker unavailable"))

			err := sender.SendCommand(ctx, validCmd)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("send SetTaskLinkStatusCommand to Kafka"))
			Expect(err.Error()).To(ContainSubstring("broker unavailable"))
			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(1))
		})
	})

	Context("downstream is fed the correct command bytes", func() {
		It("round-trips the body through the executor into a success status", func() {
			fakeCDB.SendCommandObjectReturns(nil)
			cmd := command.SetTaskLinkStatusCommand{
				URL:     "https://github.com/bborbe/repo/pull/42",
				HeadSHA: "abc123",
				Body:    "Summary\r\n\r\nTask: " + taskUUID + "\r\n",
			}

			Expect(sender.SendCommand(ctx, cmd)).To(Succeed())

			_, obj := fakeCDB.SendCommandObjectArgsForCall(0)
			Expect(obj.Command.Data).NotTo(BeNil())

			ghClient := new(mocks.GitHubClient)
			executor := command.NewSetTaskLinkStatusCommandExecutor(ghClient)
			_, _, err := executor.HandleCommand(ctx, nil, obj)
			Expect(err).NotTo(HaveOccurred())

			Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
			_, _, _, _, state, _ := ghClient.PostCommitStatusArgsForCall(0)
			Expect(state).To(Equal(pkg.CommitStateSuccess))
		})
	})

	// Silence unused-import warnings if the implementation evolves.
	_ = cdb.CommandObject{}
})
