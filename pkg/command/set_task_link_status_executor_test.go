// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command_test

import (
	"context"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	lib "github.com/bborbe/maintainer"
)

func mustParseTaskLinkEvent(cmd command.SetTaskLinkStatusCommand) base.Event {
	evt, err := base.ParseEvent(context.Background(), cmd)
	Expect(err).NotTo(HaveOccurred())
	return evt
}

func newTaskLinkCommandObject(cmd command.SetTaskLinkStatusCommand) cdb.CommandObject {
	return cdb.CommandObject{
		Command: base.Command{
			Operation: command.SetTaskLinkStatusCommandOperation,
			Data:      mustParseTaskLinkEvent(cmd),
		},
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
}

var _ = Describe("SetTaskLinkStatusCommandExecutor", func() {
	const taskUUID = "0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0"

	var (
		ctx      context.Context
		ghClient *mocks.GitHubClient
		executor cdb.CommandObjectExecutorTx
	)

	BeforeEach(func() {
		ctx = context.Background()
		ghClient = new(mocks.GitHubClient)
		executor = command.NewSetTaskLinkStatusCommandExecutor(ghClient)
	})

	// run drives one command through the public executor. The executor ignores
	// its transaction, so a nil Tx is safe here.
	run := func(cmd command.SetTaskLinkStatusCommand) error {
		_, _, err := executor.HandleCommand(ctx, nil, newTaskLinkCommandObject(cmd))
		return err
	}

	It("posts success when the body has a task line", func() {
		Expect(run(command.SetTaskLinkStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
			Body:    "Summary\n\nTask: " + taskUUID + "\n",
		})).To(Succeed())

		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
		_, owner, repo, sha, state, statusContext := ghClient.PostCommitStatusArgsForCall(0)
		Expect(owner).To(Equal("bborbe"))
		Expect(repo).To(Equal("repo"))
		Expect(sha).To(Equal("abc123"))
		Expect(state).To(Equal(pkg.CommitStateSuccess))
		Expect(statusContext).To(Equal(command.TaskLinkStatusContext))
	})

	It("posts failure when the body has no task line", func() {
		Expect(run(command.SetTaskLinkStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
			Body:    "Summary only, no task reference",
		})).To(Succeed())

		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
		_, _, _, _, state, _ := ghClient.PostCommitStatusArgsForCall(0)
		Expect(state).To(Equal(pkg.CommitStateFailure))
	})

	It("posts failure for an empty body", func() {
		Expect(run(command.SetTaskLinkStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
		})).To(Succeed())

		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
		_, _, _, _, state, _ := ghClient.PostCommitStatusArgsForCall(0)
		Expect(state).To(Equal(pkg.CommitStateFailure))
	})

	It("skips without retrying on a permanent failure", func() {
		ghClient.PostCommitStatusReturns(
			errors.Wrapf(ctx, pkg.ErrPermanent, "403 permission denied"),
		)

		err := run(command.SetTaskLinkStatusCommand{URL: validPRURL, HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
	})

	It("returns a retryable error on a transient failure", func() {
		ghClient.PostCommitStatusReturns(errors.Errorf(ctx, "502 bad gateway"))

		err := run(command.SetTaskLinkStatusCommand{URL: validPRURL, HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
	})

	It("skips a command whose url does not parse", func() {
		err := run(command.SetTaskLinkStatusCommand{URL: "not-a-url", HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(0))
	})

	It("skips a command with no head SHA", func() {
		err := run(command.SetTaskLinkStatusCommand{URL: validPRURL})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(0))
	})

	It("skips a malformed payload", func() {
		evt, err := base.ParseEvent(ctx, map[string]any{
			"url":      validPRURL,
			"head_sha": 42,
		})
		Expect(err).NotTo(HaveOccurred())
		obj := cdb.CommandObject{
			Command: base.Command{
				Operation: command.SetTaskLinkStatusCommandOperation,
				Data:      evt,
			},
			SchemaID: lib.GithubPRReviewV1SchemaID,
		}

		_, _, err = executor.HandleCommand(ctx, nil, obj)

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(0))
	})
})
