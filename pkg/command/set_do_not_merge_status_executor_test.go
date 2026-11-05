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

func mustParseStatusEvent(cmd command.SetDoNotMergeStatusCommand) base.Event {
	evt, err := base.ParseEvent(context.Background(), cmd)
	Expect(err).NotTo(HaveOccurred())
	return evt
}

func newStatusCommandObject(cmd command.SetDoNotMergeStatusCommand) cdb.CommandObject {
	return cdb.CommandObject{
		Command: base.Command{
			Operation: command.SetDoNotMergeStatusCommandOperation,
			Data:      mustParseStatusEvent(cmd),
		},
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
}

var _ = Describe("SetDoNotMergeStatusCommandExecutor", func() {
	var (
		ctx      context.Context
		ghClient *mocks.GitHubClient
		executor cdb.CommandObjectExecutorTx
	)

	BeforeEach(func() {
		ctx = context.Background()
		ghClient = new(mocks.GitHubClient)
		executor = command.NewSetDoNotMergeStatusCommandExecutor(ghClient)
	})

	// run drives one command through the public executor. The executor ignores
	// its transaction, so a nil Tx is safe here.
	run := func(cmd command.SetDoNotMergeStatusCommand) error {
		_, _, err := executor.HandleCommand(ctx, nil, newStatusCommandObject(cmd))
		return err
	}

	It("posts success when the do-not-merge label is absent", func() {
		Expect(run(command.SetDoNotMergeStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
		})).To(Succeed())

		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
		_, owner, repo, sha, state, statusContext := ghClient.PostCommitStatusArgsForCall(0)
		Expect(owner).To(Equal("bborbe"))
		Expect(repo).To(Equal("repo"))
		Expect(sha).To(Equal("abc123"))
		Expect(state).To(Equal(pkg.CommitStateSuccess))
		Expect(statusContext).To(Equal(command.DoNotMergeStatusContext))
	})

	It("posts failure when the do-not-merge label is present", func() {
		Expect(run(command.SetDoNotMergeStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
			Labels:  []string{"override-review", "do-not-merge"},
		})).To(Succeed())

		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(1))
		_, _, _, _, state, _ := ghClient.PostCommitStatusArgsForCall(0)
		Expect(state).To(Equal(pkg.CommitStateFailure))
	})

	It("posts success when the label set is non-empty but carries no do-not-merge", func() {
		Expect(run(command.SetDoNotMergeStatusCommand{
			URL:     validPRURL,
			HeadSHA: "abc123",
			Labels:  []string{"override-review", "size/large"},
		})).To(Succeed())

		_, _, _, _, state, _ := ghClient.PostCommitStatusArgsForCall(0)
		Expect(state).To(Equal(pkg.CommitStateSuccess))
	})

	It("skips without retrying on a permanent failure", func() {
		ghClient.PostCommitStatusReturns(
			errors.Wrapf(ctx, pkg.ErrPermanent, "403 permission denied"),
		)

		err := run(command.SetDoNotMergeStatusCommand{URL: validPRURL, HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
	})

	It("returns a retryable error on a transient failure", func() {
		ghClient.PostCommitStatusReturns(errors.Errorf(ctx, "502 bad gateway"))

		err := run(command.SetDoNotMergeStatusCommand{URL: validPRURL, HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
	})

	It("skips a command whose url does not parse", func() {
		err := run(command.SetDoNotMergeStatusCommand{URL: "not-a-url", HeadSHA: "abc123"})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(0))
	})

	It("skips a command with no head SHA", func() {
		err := run(command.SetDoNotMergeStatusCommand{URL: validPRURL})

		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		Expect(ghClient.PostCommitStatusCallCount()).To(Equal(0))
	})
})
