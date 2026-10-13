// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/reviewignore"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pkg.UnreviewedScanner", func() {
	var (
		ctx             context.Context
		cancel          context.CancelFunc
		ghClient        *mocks.GitHubClient
		fakeMetrics     *mocks.Metrics
		now             libtime.DateTime
		currentDateTime libtime.CurrentDateTimeGetter
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		ghClient = new(mocks.GitHubClient)
		fakeMetrics = new(mocks.Metrics)
		now = libtime.DateTime(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
		currentDateTime = libtime.CurrentDateTimeGetterFunc(
			func() libtime.DateTime { return now },
		)
	})

	AfterEach(func() { cancel() })

	// openPR builds a search-result PR. HeadSHA is empty, exactly as the real
	// Search API returns it — the head SHA comes only from GetPRDetails.
	openPR := func(
		number int,
		author string,
		updatedAt libtime.DateTime,
		labels ...string,
	) pkg.PullRequest {
		return pkg.PullRequest{
			Number:      number,
			Owner:       "bborbe",
			Repo:        "repo",
			Title:       "feat: something",
			HTMLURL:     fmt.Sprintf("https://github.com/bborbe/repo/pull/%d", number),
			AuthorLogin: author,
			UpdatedAt:   updatedAt,
			Labels:      labels,
		}
	}

	searchOpenReturns := func(prs ...pkg.PullRequest) {
		ghClient.SearchOpenPRsReturns(pkg.SearchResult{
			PullRequests:  prs,
			RateRemaining: 100,
		}, nil)
	}

	// The real filter chain and trust leaf, not fakes, so the scan's
	// qualification matches processPR's — including the github.com/<o>/<r>
	// RepoKey the allowlist filter sees.
	defaultFilter := func() filter.TaskCreationFilter {
		return filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"dependabot[bot]"}),
			filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"}),
		}
	}
	trustAlice := func() trust.Trust { return trust.NewAuthorAllowlist([]string{"alice"}) }

	newScanner := func(
		cfg pkg.UnreviewedConfig,
		f filter.TaskCreationFilter,
		t trust.Trust,
	) pkg.UnreviewedScanner {
		return pkg.NewUnreviewedScanner(
			ghClient, fakeMetrics, currentDateTime, "bborbe",
			f, t, "override-review", 0, 0, cfg,
		)
	}

	qualifying := func() pkg.UnreviewedConfig {
		return pkg.UnreviewedConfig{
			Logins:    []string{"octopus-pr-reviewer"},
			Threshold: 30 * libtime.Minute,
		}
	}

	// headDetails is the default GetPRDetails response for a countable PR.
	headDetails := func() pkg.PRDetails {
		return pkg.PRDetails{
			HeadSHA:  "sha-head",
			CloneURL: "https://github.com/bborbe/repo.git",
			BaseRef:  "master",
		}
	}

	BeforeEach(func() {
		ghClient.GetPRDetailsReturns(headDetails(), nil)
		ghClient.ListReviewsReturns(nil, nil)
	})

	It("does nothing when no reviewer logins are configured", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		s := newScanner(pkg.UnreviewedConfig{Threshold: 30 * libtime.Minute},
			defaultFilter(), trustAlice())
		s.Scan(ctx)
		Expect(ghClient.SearchOpenPRsCallCount()).To(Equal(0))
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
	})

	It("does not count a PR that has waited less than the threshold", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-29*libtime.Minute)))
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(1))
		prs, scannedAt := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
		Expect(scannedAt).To(Equal(now))
	})

	It("counts a PR that has waited exactly the threshold", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-30*libtime.Minute)))
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		prs, scannedAt := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(Equal([]pkg.UnreviewedPR{{Repo: "bborbe/repo", Number: 42}}))
		Expect(scannedAt).To(Equal(now))
	})

	It("counts a PR past the threshold with no reviews", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(1))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(Equal([]pkg.UnreviewedPR{{Repo: "bborbe/repo", Number: 42}}))
	})

	It("does not count a PR whose head SHA has a reviewer-bot review", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns([]pkg.Review{
			{AuthorLogin: "octopus-pr-reviewer[bot]", CommitID: "sha-head"},
		}, nil)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("counts a PR when the reviewer-bot review is only on an older SHA", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns([]pkg.Review{
			{AuthorLogin: "octopus-pr-reviewer[bot]", CommitID: "old-sha"},
		}, nil)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(Equal([]pkg.UnreviewedPR{{Repo: "bborbe/repo", Number: 42}}))
	})

	It("counts a PR reviewed on the head SHA by a non-bot login", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns([]pkg.Review{
			{AuthorLogin: "alice", CommitID: "sha-head"},
		}, nil)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(Equal([]pkg.UnreviewedPR{{Repo: "bborbe/repo", Number: 42}}))
	})

	It("matches a reviewer-bot review whose login carries the [bot] suffix", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns([]pkg.Review{
			{AuthorLogin: "OCTOPUS-PR-REVIEWER[BOT]", CommitID: "sha-head"},
		}, nil)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("matches when the configured login carries the [bot] suffix", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns([]pkg.Review{
			{AuthorLogin: "octopus-pr-reviewer", CommitID: "sha-head"},
		}, nil)
		cfg := pkg.UnreviewedConfig{
			Logins:    []string{"octopus-pr-reviewer[bot]"},
			Threshold: 30 * libtime.Minute,
		}
		newScanner(cfg, defaultFilter(), trustAlice()).Scan(ctx)
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("skips a draft PR without fetching details", func() {
		pr := openPR(42, "alice", now.Add(-libtime.Hour))
		pr.IsDraft = true
		searchOpenReturns(pr)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("skips a PR outside the repo allowlist without fetching details", func() {
		pr := openPR(42, "alice", now.Add(-libtime.Hour))
		pr.Owner = "other"
		searchOpenReturns(pr)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("skips an untrusted author without fetching details", func() {
		searchOpenReturns(openPR(42, "mallory", now.Add(-libtime.Hour)))
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("skips a PR whose trust check errors without aborting the scan", func() {
		searchOpenReturns(
			openPR(42, "alice", now.Add(-libtime.Hour)),
			openPR(43, "alice", now.Add(-libtime.Hour)),
		)
		trustErr := trust.Func(func(
			_ context.Context,
			pr trust.PR,
		) (trust.Result, error) {
			if pr.AuthorLogin == "alice" {
				return nil, errors.Errorf(ctx, "trust backend down")
			}
			return trust.NewResult(true, "trusted"), nil
		})
		newScanner(qualifying(), defaultFilter(), trustErr).Scan(ctx)
		// Both PRs error on trust → both skipped, scan completes with an empty set.
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(1))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	It("skips a PR carrying the override label without fetching details", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour), "override-review"))
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.GetPRDetailsCallCount()).To(Equal(0))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	Describe("oversized PRs", func() {
		oversizedScanner := func(maxAdditions int) pkg.UnreviewedScanner {
			return pkg.NewUnreviewedScanner(
				ghClient, fakeMetrics, currentDateTime, "bborbe",
				defaultFilter(), trustAlice(), "override-review", maxAdditions, 0, qualifying(),
			)
		}

		It("does not count a PR PublishCreate would park", func() {
			searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
			details := headDetails()
			details.Additions = 200
			ghClient.GetPRDetailsReturns(details, nil)
			// No `.reviewignore` patterns → exclusion is empty → still oversized.
			ghClient.GetReviewIgnoreReturns(reviewignore.Parse(nil), nil)
			oversizedScanner(100).Scan(ctx)
			Expect(ghClient.GetReviewIgnoreCallCount()).To(Equal(1))
			Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
			prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
			Expect(prs).To(BeEmpty())
		})

		It("counts a raw-oversized PR that .reviewignore brings under the limit", func() {
			searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
			details := headDetails()
			details.Additions = 200
			details.ChangedFiles = 3
			ghClient.GetPRDetailsReturns(details, nil)
			matcher := new(mocks.ReviewIgnoreMatcher)
			matcher.EmptyReturns(false)
			matcher.MatchReturns(true)
			ghClient.GetReviewIgnoreReturns(matcher, nil)
			ghClient.ListPRFilesReturns([]pkg.PRFile{
				{Filename: "vendor/big.go", Additions: 150},
			}, nil)
			oversizedScanner(100).Scan(ctx)
			Expect(ghClient.GetReviewIgnoreCallCount()).To(Equal(1))
			Expect(ghClient.ListReviewsCallCount()).To(Equal(1))
			prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
			Expect(prs).To(Equal([]pkg.UnreviewedPR{{Repo: "bborbe/repo", Number: 42}}))
		})

		It("does not consult .reviewignore for an under-limit PR", func() {
			searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
			details := headDetails()
			details.Additions = 50
			ghClient.GetPRDetailsReturns(details, nil)
			oversizedScanner(100).Scan(ctx)
			Expect(ghClient.GetReviewIgnoreCallCount()).To(Equal(0))
			Expect(ghClient.ListPRFilesCallCount()).To(Equal(0))
		})
	})

	It("skips a PR whose details carry no head SHA", func() {
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(pkg.PRDetails{HeadSHA: ""}, nil)
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(ghClient.ListReviewsCallCount()).To(Equal(0))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	Describe("aborting scans leave the previous metric untouched", func() {
		It("does not publish when the second PR's ListReviews errors", func() {
			searchOpenReturns(
				openPR(42, "alice", now.Add(-libtime.Hour)),
				openPR(43, "alice", now.Add(-libtime.Hour)),
			)
			ghClient.ListReviewsReturnsOnCall(1, nil, errors.Errorf(ctx, "boom"))
			newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
			Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
		})

		It("does not publish when GetPRDetails errors", func() {
			searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
			ghClient.GetPRDetailsReturns(pkg.PRDetails{}, errors.Errorf(ctx, "boom"))
			newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
			Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
		})

		It("does not publish when the second search page errors", func() {
			ghClient.SearchOpenPRsReturnsOnCall(0, pkg.SearchResult{
				PullRequests: []pkg.PullRequest{openPR(42, "alice", now.Add(-libtime.Hour))},
				HasNextPage:  true,
				NextPage:     2,
			}, nil)
			ghClient.SearchOpenPRsReturnsOnCall(1, pkg.SearchResult{},
				errors.Errorf(ctx, "boom"))
			newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
			Expect(ghClient.SearchOpenPRsCallCount()).To(Equal(2))
			Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
		})

		It("does not publish when the context is cancelled before the first page", func() {
			searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
			cancel()
			newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
			Expect(ghClient.SearchOpenPRsCallCount()).To(Equal(0))
			Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
		})

		It("does not publish when the context is cancelled mid-loop", func() {
			searchOpenReturns(
				openPR(42, "alice", now.Add(-libtime.Hour)),
				openPR(43, "alice", now.Add(-libtime.Hour)),
			)
			ghClient.ListReviewsStub = func(
				context.Context, string, string, int,
			) ([]pkg.Review, error) {
				cancel()
				return nil, nil
			}
			newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
			Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
		})
	})

	It("never fails the poll cycle when the scan aborts", func() {
		tmpDir, err := os.MkdirTemp("", "unreviewed-poll-*")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = os.RemoveAll(tmpDir) }()
		cursorPath := filepath.Join(tmpDir, "cursor.json")

		ghClient.SearchPRsReturns(pkg.SearchResult{RateRemaining: 100}, nil)
		searchOpenReturns(openPR(42, "alice", now.Add(-libtime.Hour)))
		ghClient.ListReviewsReturns(nil, errors.Errorf(ctx, "boom"))

		scanner := pkg.NewUnreviewedScanner(
			ghClient, fakeMetrics, currentDateTime, "bborbe",
			defaultFilter(), trustAlice(), "override-review", 0, 0, qualifying(),
		)
		publisher := new(mocks.TaskPublisher)
		w := pkg.NewWatcher(
			ghClient, publisher, fakeMetrics, cursorPath, now, "bborbe",
			defaultFilter(), defaultFilter(), "override-review", "", false,
			trustAlice(), scanner,
		)

		Expect(w.Poll(ctx)).To(Succeed())
		Expect(fakeMetrics.IncPollCycleArgsForCall(0)).To(Equal("success"))
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(0))
	})

	It("publishes an empty set when nothing qualifies", func() {
		searchOpenReturns()
		newScanner(qualifying(), defaultFilter(), trustAlice()).Scan(ctx)
		Expect(fakeMetrics.SetUnreviewedPRsCallCount()).To(Equal(1))
		prs, _ := fakeMetrics.SetUnreviewedPRsArgsForCall(0)
		Expect(prs).To(BeEmpty())
	})

	Describe("ParseUnreviewedConfig", func() {
		It("yields no logins for a blank login list", func() {
			cfg, err := pkg.ParseUnreviewedConfig(ctx, " , ", "30m")
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Logins).To(BeEmpty())
			Expect(cfg.Threshold).To(Equal(30 * libtime.Minute))
		})

		It("parses a comma-separated login list", func() {
			cfg, err := pkg.ParseUnreviewedConfig(
				ctx,
				"octopus-pr-reviewer, octopus-pr-reviewer-dev",
				"45m",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Logins).To(Equal([]string{
				"octopus-pr-reviewer",
				"octopus-pr-reviewer-dev",
			}))
			Expect(cfg.Threshold).To(Equal(45 * libtime.Minute))
		})

		DescribeTable("rejects invalid thresholds",
			func(raw string) {
				_, err := pkg.ParseUnreviewedConfig(ctx, "octopus-pr-reviewer", raw)
				Expect(err).To(HaveOccurred())
			},
			Entry("zero", "0s"),
			Entry("negative", "-1m"),
			Entry("unparseable", "abc"),
		)
	})
})
