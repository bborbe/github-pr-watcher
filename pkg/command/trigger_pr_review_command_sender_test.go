// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	cdbmocks "github.com/bborbe/cqrs/mocks"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	lib "github.com/bborbe/maintainer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newTestCommandCreator returns a CommandCreator backed by a buffered channel
// pre-populated with `n` request IDs. Suitable for unit tests that don't
// want to plumb base.RequestIDChannel(ctx) and risk the channel blocking.
func newTestCommandCreator(n int) base.CommandCreator {
	ch := make(chan base.RequestID, n)
	for i := 0; i < n; i++ {
		ch <- base.NewRequestID()
	}
	return base.NewCommandCreator(ch)
}

var _ = Describe("TriggerPRReviewCommandSender.SendCommand", func() {
	var (
		ctx      context.Context
		fakeCDB  *cdbmocks.CDBCommandObjectSender
		sender   command.TriggerPRReviewCommandSender
		validCmd command.TriggerPRReviewCommand
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeCDB = new(cdbmocks.CDBCommandObjectSender)
		sender = command.NewTriggerPRReviewCommandSender(
			newTestCommandCreator(10),
			cqrsiam.Initiator("test-watcher"),
			fakeCDB,
		)
		validCmd = command.TriggerPRReviewCommand{
			URL:   "https://github.com/bborbe/repo/pull/42",
			Force: false,
		}
	})

	Context("valid command", func() {
		It("publishes one CommandObject with the correct operation and SchemaID", func() {
			fakeCDB.SendCommandObjectReturns(nil)

			Expect(sender.SendCommand(ctx, validCmd)).To(Succeed())

			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(1))
			_, obj := fakeCDB.SendCommandObjectArgsForCall(0)
			Expect(obj.SchemaID).To(Equal(lib.GithubPRReviewV1SchemaID))
			Expect(obj.Command.Operation).To(Equal(command.TriggerPRReviewCommandOperation))
		})
	})

	Context("validation fails", func() {
		It("returns a wrapped validation error and does NOT touch Kafka", func() {
			invalid := command.TriggerPRReviewCommand{URL: ""}

			err := sender.SendCommand(ctx, invalid)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("validate TriggerPRReviewCommand"))
			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(0))
		})
	})

	Context("Kafka publish fails", func() {
		It("returns a wrapped Kafka error", func() {
			fakeCDB.SendCommandObjectReturns(errors.Errorf(ctx, "broker unavailable"))

			err := sender.SendCommand(ctx, validCmd)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("send TriggerPRReviewCommand to Kafka"))
			Expect(err.Error()).To(ContainSubstring("broker unavailable"))
			Expect(fakeCDB.SendCommandObjectCallCount()).To(Equal(1))
		})
	})

	Context("downstream is fed the correct command bytes", func() {
		It("populates the event payload from the command via base.ParseEvent", func() {
			// Round-trip via the cdb command's Data: the event the sender
			// constructed must round-trip back to the original TriggerPRReviewCommand.
			fakeCDB.SendCommandObjectReturns(nil)

			Expect(sender.SendCommand(ctx, validCmd)).To(Succeed())

			_, obj := fakeCDB.SendCommandObjectArgsForCall(0)
			Expect(obj.Command.Data).NotTo(BeNil())
			var roundTripped command.TriggerPRReviewCommand
			Expect(obj.Command.Data.MarshalInto(ctx, &roundTripped)).To(Succeed())
			Expect(roundTripped.URL).To(Equal(validCmd.URL))
			Expect(roundTripped.Force).To(Equal(validCmd.Force))
		})
	})

	// Silence unused-import warnings if the implementation evolves.
	_ = cdb.CommandObject{}
})
