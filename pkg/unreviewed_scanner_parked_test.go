// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pkg.UnreviewedScanner parked PR metric", func() {
	var (
		ctx             context.Context
		cancel          context.CancelFunc
		ghClient        *mocks.GitHubClient
		publisher       *mocks.TaskPublisher
		fakeMetrics     *mocks.Metrics
		now             libtime.DateTime
		currentDateTime libtime.CurrentDateTimeGetter
		tmpDir          string
		ledgerPath      string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		ghClient = new(mocks.GitHubClient)
		publisher = new(mocks.TaskPublisher)
		publisher.PublishCreateReturns(true)
		publisher.PublishRedriveReturns(true)
		fakeMetrics = new(mocks.Metrics)
		now = libtime.DateTime(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
		currentDateTime = libtime.CurrentDateTimeGetterFunc(
			func() libtime.DateTime { return now },
		)
		var err error
		tmpDir, err = os.MkdirTemp("", "unreviewed-parked-*")
		Expect(err).NotTo(HaveOccurred())
		ledgerPath = filepath.Join(tmpDir, "park-ledger.json")
	})

	AfterEach(func() {
		cancel()
		_ = os.RemoveAll(tmpDir)
	})

	openPR := func(number int, updatedAt libtime.DateTime) pkg.PullRequest {
		return pkg.PullRequest{
			Number:      number,
			Owner:       "bborbe",
			Repo:        "repo",
			Title:       "feat: something",
			HTMLURL:     fmt.Sprintf("https://github.com/bborbe/repo/pull/%d", number),
			AuthorLogin: "alice",
			UpdatedAt:   updatedAt,
		}
	}

	searchOpenReturns := func(prs ...pkg.PullRequest) {
		ghClient.SearchOpenPRsReturns(pkg.SearchResult{
			PullRequests:  prs,
			RateRemaining: 100,
		}, nil)
	}

	defaultFilter := func() filter.TaskCreationFilter {
		return filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"dependabot[bot]"}),
			filter.NewRepoAllowlistFilter([]string{"github.com/bborbe/*"}),
		}
	}

	cfg := func() pkg.UnreviewedConfig {
		return pkg.UnreviewedConfig{
			Logins:    []string{"octopus-pr-reviewer"},
			Threshold: 30 * libtime.Minute,
			RetryMax:  3,
		}
	}

	// newScanner wires a ledger at the shared path. maxAdditions is 10, so a PR
	// with more than 10 added lines is oversized; the default GetReviewIgnore
	// fake returns (nil, nil), so the exclusion is empty.
	newScanner := func() pkg.UnreviewedScanner {
		return pkg.NewUnreviewedScanner(
			ghClient, publisher, pkg.NewDispatchLedger(), fakeMetrics, currentDateTime,
			"bborbe", defaultFilter(), trust.NewAuthorAllowlist([]string{"alice"}),
			"override-review", 10, 0, cfg(),
			pkg.WithParkLedger(pkg.NewParkLedger(ledgerPath)),
		)
	}

	details := func(sha string, additions int) pkg.PRDetails {
		return pkg.PRDetails{
			HeadSHA:      sha,
			Additions:    additions,
			ChangedFiles: 1,
			CloneURL:     "https://github.com/bborbe/repo.git",
			BaseRef:      "master",
		}
	}

	type ledgerEntry struct {
		Start string `json:"start"`
		SHA   string `json:"sha"`
	}
	readLedger := func() map[string]ledgerEntry {
		data, err := os.ReadFile(ledgerPath)
		Expect(err).NotTo(HaveOccurred())
		var file struct {
			Entries map[string]ledgerEntry `json:"entries"`
		}
		Expect(json.Unmarshal(data, &file)).To(Succeed())
		return file.Entries
	}

	It("publishes a parked oversized PR with its age", func() {
		searchOpenReturns(openPR(42, now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)

		newScanner().Scan(ctx)

		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(1))
		parked := fakeMetrics.SetParkedPRsArgsForCall(0)
		Expect(parked).To(HaveLen(1))
		Expect(parked[0].Repo).To(Equal("bborbe/repo"))
		Expect(parked[0].Number).To(Equal(42))
		Expect(parked[0].AgeSeconds).To(BeNumerically("~", 3600, 60))

		entries := readLedger()
		Expect(entries).To(HaveKey("bborbe/repo#42"))
		Expect(entries["bborbe/repo#42"].SHA).To(Equal("sha-a"))
	})

	It("does not reset the age when a comment moves updated-at", func() {
		searchOpenReturns(openPR(42, now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)
		newScanner().Scan(ctx)

		// A comment moves updated-at but leaves the head SHA unchanged.
		searchOpenReturns(openPR(42, now))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)
		newScanner().Scan(ctx)

		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(2))
		parked := fakeMetrics.SetParkedPRsArgsForCall(1)
		Expect(parked).To(HaveLen(1))
		Expect(parked[0].AgeSeconds).To(BeNumerically("~", 3600, 60))
	})

	It("clears the series within one scan when a push brings it under the limit", func() {
		searchOpenReturns(openPR(42, now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)
		newScanner().Scan(ctx)
		Expect(fakeMetrics.SetParkedPRsArgsForCall(0)).To(HaveLen(1))

		// A push moves updated-at and changes the head SHA; the new diff is
		// under the size limit.
		searchOpenReturns(openPR(42, now))
		ghClient.GetPRDetailsReturns(details("sha-b", 2), nil)
		newScanner().Scan(ctx)

		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(2))
		Expect(fakeMetrics.SetParkedPRsArgsForCall(1)).To(BeEmpty())
		Expect(readLedger()).NotTo(HaveKey("bborbe/repo#42"))
	})

	It("clears the series when the PR is closed", func() {
		searchOpenReturns(openPR(42, now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)
		newScanner().Scan(ctx)

		searchOpenReturns()
		newScanner().Scan(ctx)

		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(2))
		Expect(fakeMetrics.SetParkedPRsArgsForCall(1)).To(BeEmpty())
	})

	It("leaves the previous values when a scan aborts", func() {
		searchOpenReturns(openPR(42, now.Add(-libtime.Hour)))
		ghClient.GetPRDetailsReturns(details("sha-a", 20), nil)
		newScanner().Scan(ctx)
		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(1))

		searchOpenReturns(openPR(42, now))
		ghClient.GetPRDetailsReturns(pkg.PRDetails{}, errors.Errorf(ctx, "boom"))
		newScanner().Scan(ctx)

		Expect(fakeMetrics.SetParkedPRsCallCount()).To(Equal(1))
	})
})
