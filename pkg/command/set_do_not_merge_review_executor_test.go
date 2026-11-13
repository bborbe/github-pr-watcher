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

func mustParseReviewEvent(cmd command.SetDoNotMergeReviewCommand) base.Event {
	evt, err := base.ParseEvent(context.Background(), cmd)
	Expect(err).NotTo(HaveOccurred())
	return evt
}

func newReviewCommandObject(cmd command.SetDoNotMergeReviewCommand) cdb.CommandObject {
	return cdb.CommandObject{
		Command: base.Command{
			Operation: command.SetDoNotMergeReviewCommandOperation,
			Data:      mustParseReviewEvent(cmd),
		},
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
}

// gateReview builds a review this mechanism posted, on the given SHA.
func gateReview(id int64, state pkg.ReviewState) pkg.Review {
	return pkg.Review{
		ID:       id,
		Body:     command.DoNotMergeReviewBody,
		State:    state,
		CommitID: "abc123",
	}
}

// genuineReview builds a review the reviewer bot posted for real findings — the
// one thing the dismissal path must never touch.
func genuineReview(id int64, state pkg.ReviewState) pkg.Review {
	return pkg.Review{
		ID:       id,
		Body:     "Found two real defects in the parser.",
		State:    state,
		CommitID: "abc123",
	}
}

var _ = Describe("SetDoNotMergeReviewCommandExecutor", func() {
	var (
		ctx      context.Context
		ghClient *mocks.GitHubClient
		executor cdb.CommandObjectExecutorTx
	)

	BeforeEach(func() {
		ctx = context.Background()
		ghClient = new(mocks.GitHubClient)
		executor = command.NewSetDoNotMergeReviewCommandExecutor(ghClient)
	})

	run := func(cmd command.SetDoNotMergeReviewCommand) error {
		_, _, err := executor.HandleCommand(ctx, nil, newReviewCommandObject(cmd))
		return err
	}

	// The label predicate is the whole mechanism: a handler that submits on
	// every PR would block every merge in the fleet. Both directions are
	// asserted, because only the pair distinguishes "gated" from "always".
	Describe("the label predicate", func() {
		It("submits a CHANGES_REQUESTED review when the label is present", func() {
			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"override-review", "do-not-merge"},
			})).To(Succeed())

			Expect(ghClient.SubmitReviewCallCount()).To(Equal(1))
			_, owner, repo, number, commitID, body, event := ghClient.SubmitReviewArgsForCall(0)
			Expect(owner).To(Equal("bborbe"))
			Expect(repo).To(Equal("repo"))
			Expect(number).To(Equal(42))
			Expect(commitID).To(Equal("abc123"))
			Expect(body).To(Equal(command.DoNotMergeReviewBody))
			Expect(event).To(Equal(pkg.ReviewStateChangesRequested))
			Expect(ghClient.DismissReviewCallCount()).To(Equal(0))
		})

		It("submits nothing when the label is absent", func() {
			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"override-review", "size/large"},
			})).To(Succeed())

			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
			Expect(ghClient.DismissReviewCallCount()).To(Equal(0))
		})

		It("submits nothing when there are no labels at all", func() {
			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})).To(Succeed())

			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
		})
	})

	Describe("idempotency on the blocking path", func() {
		It("does not submit a second gate review when one is already standing", func() {
			ghClient.ListReviewsReturns([]pkg.Review{
				gateReview(11, pkg.ReviewStateChangesRequested),
			}, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"do-not-merge"},
			})).To(Succeed())

			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
		})

		It("submits when the only existing CHANGES_REQUESTED is the bot's own finding", func() {
			// The bot's genuine review blocks the merge too, but it is not the
			// gate's review — treating it as one would leave a labelled PR with
			// no gate review of its own, and the gate would appear to work.
			ghClient.ListReviewsReturns([]pkg.Review{
				genuineReview(12, pkg.ReviewStateChangesRequested),
			}, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"do-not-merge"},
			})).To(Succeed())

			Expect(ghClient.SubmitReviewCallCount()).To(Equal(1))
		})
	})

	Describe("the label-removal path", func() {
		It("dismisses the standing gate review", func() {
			ghClient.ListReviewsReturns([]pkg.Review{
				gateReview(11, pkg.ReviewStateChangesRequested),
			}, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})).To(Succeed())

			Expect(ghClient.DismissReviewCallCount()).To(Equal(1))
			_, owner, repo, number, reviewID, message := ghClient.DismissReviewArgsForCall(0)
			Expect(owner).To(Equal("bborbe"))
			Expect(repo).To(Equal("repo"))
			Expect(number).To(Equal(42))
			Expect(reviewID).To(Equal(int64(11)))
			Expect(message).To(Equal(command.DoNotMergeReviewDismissalMessage))
			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
		})

		It("never dismisses the reviewer bot's genuine finding", func() {
			// Marker scoping is load-bearing: without it, removing the label
			// would silently erase a real CHANGES_REQUESTED verdict.
			ghClient.ListReviewsReturns([]pkg.Review{
				genuineReview(12, pkg.ReviewStateChangesRequested),
			}, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})).To(Succeed())

			Expect(ghClient.DismissReviewCallCount()).To(Equal(0))
		})

		It("dismisses only the gate review when both kinds are present", func() {
			ghClient.ListReviewsReturns([]pkg.Review{
				genuineReview(12, pkg.ReviewStateChangesRequested),
				gateReview(11, pkg.ReviewStateChangesRequested),
			}, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})).To(Succeed())

			Expect(ghClient.DismissReviewCallCount()).To(Equal(1))
			_, _, _, _, reviewID, _ := ghClient.DismissReviewArgsForCall(0)
			Expect(reviewID).To(Equal(int64(11)))
		})

		It("does nothing when no gate review is standing", func() {
			ghClient.ListReviewsReturns(nil, nil)

			Expect(run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})).To(Succeed())

			Expect(ghClient.DismissReviewCallCount()).To(Equal(0))
		})
	})

	Describe("error handling", func() {
		It("skips without retrying when the submit fails permanently", func() {
			ghClient.SubmitReviewReturns(
				errors.Wrapf(ctx, pkg.ErrPermanent, "403 permission denied"),
			)

			err := run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"do-not-merge"},
			})

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
		})

		It("returns a retryable error when the submit fails transiently", func() {
			ghClient.SubmitReviewReturns(errors.Errorf(ctx, "502 bad gateway"))

			err := run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
				Labels:  []string{"do-not-merge"},
			})

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
		})

		It("returns a retryable error when the review list cannot be read", func() {
			ghClient.ListReviewsReturns(nil, errors.Errorf(ctx, "502 bad gateway"))

			err := run(command.SetDoNotMergeReviewCommand{
				URL:     validPRURL,
				HeadSHA: "abc123",
			})

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeFalse())
			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
			Expect(ghClient.DismissReviewCallCount()).To(Equal(0))
		})

		It("skips a command whose url does not parse", func() {
			err := run(command.SetDoNotMergeReviewCommand{URL: "not-a-url", HeadSHA: "abc123"})

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
			Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
		})

		It("skips a command with no head SHA", func() {
			err := run(command.SetDoNotMergeReviewCommand{
				URL:    validPRURL,
				Labels: []string{"do-not-merge"},
			})

			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, cdb.ErrCommandObjectSkipped)).To(BeTrue())
			Expect(ghClient.SubmitReviewCallCount()).To(Equal(0))
		})
	})
})
