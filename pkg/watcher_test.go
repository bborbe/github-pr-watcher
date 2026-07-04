// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	task "github.com/bborbe/agent/command/task"
	taskmocks "github.com/bborbe/agent/mocks"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newTestWatcher(
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	cursorPath string,
	startTime libtime.DateTime,
	fakeMetrics *mocks.Metrics,
	trustDecision trust.Trust,
) pkg.Watcher {
	publisher := pkg.NewTaskPublisher(
		createSender,
		trustDecision,
		fakeMetrics,
		pkg.TaskConfig{
			Stage:       "dev",
			MaxSlugLen:  pkg.DefaultMaxSlugLen,
			MaxTitleLen: pkg.DefaultMaxTitleLen,
			TaskSuffix:  "",
		},
	)
	return pkg.NewWatcher(
		ghClient,
		publisher,
		fakeMetrics,
		cursorPath,
		startTime,
		"bborbe",
		filter.TaskCreationFilters{
			filter.NewDraftFilter(),
			filter.NewBotAuthorFilter([]string{"dependabot[bot]"}),
		},
	)
}

var _ = Describe("pkg.Watcher", func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		ghClient     *mocks.GitHubClient
		createSender *taskmocks.TaskCreateCommandSender
		fakeMetrics  *mocks.Metrics
		tmpDir       string
		cursorPath   string
		startTime    libtime.DateTime
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		ghClient = new(mocks.GitHubClient)
		createSender = new(taskmocks.TaskCreateCommandSender)
		fakeMetrics = new(mocks.Metrics)
		startTime = libtime.DateTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		var err error
		tmpDir, err = os.MkdirTemp("", "watcher-test-*")
		Expect(err).NotTo(HaveOccurred())
		cursorPath = filepath.Join(tmpDir, "cursor.json")
	})

	AfterEach(func() {
		cancel()
		_ = os.RemoveAll(tmpDir) // #nosec G104 -- best-effort temp dir cleanup
	})

	Describe("No PRs returned", func() {
		It("returns nil, cursor saved", func() {
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  nil,
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
			_, err = os.Stat(cursorPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeMetrics.IncPollCycleCallCount()).To(Equal(1))
			result := fakeMetrics.IncPollCycleArgsForCall(0)
			Expect(result).To(Equal("success"))
		})
	})

	Describe("New PR (no existing cursor entry)", func() {
		It("publishes CreateTaskCommand", func() {
			pr := pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "feat: new feature",
				HTMLURL:     "https://github.com/bborbe/maintainer/pull/42",
				AuthorLogin: "alice",
				IsDraft:     false,
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "abc123",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())

			Expect(createSender.SendCommandCallCount()).To(Equal(1))

			_, cmd := createSender.SendCommandArgsForCall(0)
			Expect(string(cmd.TaskIdentifier)).NotTo(BeEmpty())
			Expect(cmd.Frontmatter["assignee"]).To(Equal("pr-reviewer-agent"))
			Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
			Expect(
				cmd.Title,
			).To(Equal("PR Review github - bborbe-code-reviewer - 42 - abc123 - feat-new-feature"))
			Expect(fakeMetrics.IncPRPublishedCallCount()).To(Equal(1))
			command := fakeMetrics.IncPRPublishedArgsForCall(0)
			Expect(command).To(Equal("create"))
		})
	})

	Describe("Existing PR, same SHA", func() {
		It("publishes nothing", func() {
			pr := pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "existing pr",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-existing",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)

			// Pre-populate cursor with the same SHA
			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			// First poll: creates the entry
			createSender.SendCommandReturns(nil)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(1))

			// Second poll: same SHA, no publish
			createSender = new(taskmocks.TaskCreateCommandSender)
			w = newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Describe("New commit on existing PR (different SHA) — per-SHA spawn", func() {
		It("publishes a new CreateTaskCommand for the new SHA", func() {
			pr := pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "force pushed pr",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}

			// First poll: SHA=old-sha → CreateTaskCommand published
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "old-sha",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(1))

			// Second poll: SHA=new-sha → new CreateTaskCommand
			createSender2 := new(taskmocks.TaskCreateCommandSender)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "new-sha",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender2.SendCommandReturns(nil)

			w2 := newTestWatcher(
				ghClient,
				createSender2,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w2.Poll(ctx)).NotTo(HaveOccurred())

			// New SHA → new CreateTaskCommand published
			Expect(createSender2.SendCommandCallCount()).To(Equal(1))
			_, cmd := createSender2.SendCommandArgsForCall(0)
			Expect(cmd.Title).To(ContainSubstring("new-sha"))
			// Old task identifier (old-sha) must differ from new task identifier (new-sha)
			_, cmd1 := createSender.SendCommandArgsForCall(0)
			Expect(string(cmd.TaskIdentifier)).NotTo(Equal(string(cmd1.TaskIdentifier)))
		})
	})

	Describe("Draft PR", func() {
		It("is skipped, no publish calls", func() {
			pr := pkg.PullRequest{
				Number:      1,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				IsDraft:     true,
				UpdatedAt:   libtime.DateTime(fixedNow),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Describe("Bot-authored PR", func() {
		It("is skipped, no publish calls", func() {
			pr := pkg.PullRequest{
				Number:      2,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "dependabot[bot]",
				IsDraft:     false,
				UpdatedAt:   libtime.DateTime(fixedNow),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Describe("GitHub error", func() {
		It("Poll returns nil, cursor unchanged, no publish calls", func() {
			ghClient.SearchPRsReturns(pkg.SearchResult{}, errors.New("network timeout"))

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
			// pkg.Cursor file should not exist (no save after error)
			_, statErr := os.Stat(cursorPath)
			Expect(os.IsNotExist(statErr)).To(BeTrue())
			Expect(fakeMetrics.IncPollCycleCallCount()).To(Equal(1))
			result := fakeMetrics.IncPollCycleArgsForCall(0)
			Expect(result).To(Equal("github_error"))
		})
	})

	Describe("Kafka publish fails (CreateTaskCommand)", func() {
		It("Poll returns nil, cursor not updated for that PR", func() {
			pr := pkg.PullRequest{
				Number:      99,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha123",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(errors.New("kafka unavailable"))

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())

			// HeadSHAs should not include this PR since publish failed
			// Verify by doing a second poll and checking SendCommand is called again
			createSender2 := new(taskmocks.TaskCreateCommandSender)
			createSender2.SendCommandReturns(nil)
			w2 := newTestWatcher(
				ghClient,
				createSender2,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w2.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender2.SendCommandCallCount()).To(Equal(1))
		})
	})

	Describe("Context cancellation between pages", func() {
		It("Poll returns nil cleanly", func() {
			cancelCtx, cancelFn := context.WithCancel(context.Background())

			callCount := 0
			ghClient.SearchPRsStub = func(c context.Context, scope string, since libtime.DateTime, page int) (pkg.SearchResult, error) {
				callCount++
				if callCount == 1 {
					cancelFn() // cancel context after first page
					return pkg.SearchResult{
						PullRequests:  nil,
						HasNextPage:   true,
						NextPage:      2,
						RateRemaining: 100,
					}, nil
				}
				return pkg.SearchResult{}, nil
			}

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(cancelCtx)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("pkg.Cursor file missing on first poll", func() {
		It("uses startTime as since, calls SearchPRs correctly", func() {
			ghClient.SearchPRsReturns(pkg.SearchResult{
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())

			Expect(ghClient.SearchPRsCallCount()).To(Equal(1))
			_, _, since, _ := ghClient.SearchPRsArgsForCall(0)
			Expect(since).To(Equal(startTime))
		})
	})

	Describe("LoadCursor error (unreadable cursor file)", func() {
		It("Poll returns a non-nil error", func() {
			if os.Getuid() == 0 {
				Skip("running as root, skipping permission test")
			}
			Expect(os.WriteFile(cursorPath, []byte("{}"), 0600)).To(Succeed())
			Expect(os.Chmod(cursorPath, 0000)).To(Succeed())
			defer func() { _ = os.Chmod(cursorPath, 0600) }()

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Closed PR pruned from cursor after poll", func() {
		It("removes closed PR's task ID from HeadSHAs after second poll", func() {
			prA := pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "pr A",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			prB := pkg.PullRequest{
				Number:      43,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "pr B",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}

			// First poll: both PRs open
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{prA, prB},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-initial",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(2))

			// Second poll: only PR A returned (PR B closed/merged)
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{prA},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			createSender2 := new(taskmocks.TaskCreateCommandSender)
			createSender2.SendCommandReturns(nil)
			w2 := newTestWatcher(
				ghClient,
				createSender2,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w2.Poll(ctx)).NotTo(HaveOccurred())

			cursor, err := pkg.LoadCursor(ctx, cursorPath, startTime)
			Expect(err).NotTo(HaveOccurred())

			taskIDA := pkg.DeriveTaskID(prA.Owner, prA.Repo, prA.Number, "sha-initial").String()
			taskIDB := pkg.DeriveTaskID(prB.Owner, prB.Repo, prB.Number, "sha-initial").String()
			Expect(cursor.HeadSHAs).To(HaveKey(taskIDA))
			Expect(cursor.HeadSHAs).NotTo(HaveKey(taskIDB))
		})
	})

	Describe("New SHA create Kafka error — cursor does not advance to new SHA", func() {
		It("does not add new SHA's task ID to cursor when publish fails, Poll returns nil", func() {
			pr := pkg.PullRequest{
				Number:      55,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}

			// Poll 1: SHA=sha-v1 → create succeeds
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-v1",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(ghClient, createSender, cursorPath, startTime, fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}))
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(1))

			// Poll 2: SHA=sha-v2 → create FAILS
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-v2",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender2 := new(taskmocks.TaskCreateCommandSender)
			createSender2.SendCommandReturns(errors.New("kafka unavailable"))
			w2 := newTestWatcher(ghClient, createSender2, cursorPath, startTime, fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}))
			Expect(w2.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender2.SendCommandCallCount()).To(Equal(1))

			// SHA-v2 task ID must NOT be in cursor after failed publish
			taskIDv2 := pkg.DeriveTaskID(pr.Owner, pr.Repo, pr.Number, "sha-v2").String()
			cursor, err := pkg.LoadCursor(ctx, cursorPath, startTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(cursor.HeadSHAs).NotTo(HaveKey(taskIDv2))
		})
	})

	Describe("fetchHeadSHA returns an error", func() {
		It("Poll returns nil, SendCommand never called", func() {
			pr := pkg.PullRequest{
				Number:      77,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(pkg.PRDetails{}, errors.New("github api error"))

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))

			// Cursor LastUpdatedAt should not advance past startTime
			cursor, err := pkg.LoadCursor(ctx, cursorPath, startTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(cursor.LastUpdatedAt).To(Equal(startTime))
		})
	})

	Describe("fetchHeadSHA cache hit with duplicate task ID", func() {
		It("calls GetPRDetails once and SendCommand once for same PR twice in results", func() {
			pr1 := pkg.PullRequest{
				Number:      10,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			// Identical Owner/Repo/Number → same task ID
			pr2 := pr1

			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr1, pr2},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-dedup",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())

			Expect(ghClient.GetPRDetailsCallCount()).To(Equal(1))
			Expect(createSender.SendCommandCallCount()).To(Equal(1))
		})
	})

	Describe("pkg.Cursor save fails", func() {
		It("Poll returns nil (non-crash)", func() {
			ghClient.SearchPRsReturns(pkg.SearchResult{
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)

			// Use an unwritable path
			w := newTestWatcher(
				ghClient,
				createSender,
				"/nonexistent/path/cursor.json",
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("GetPRDetails caches result", func() {
		It("calls GetPRDetails once per unique PR", func() {
			prs := []pkg.PullRequest{
				{
					Number:      1,
					Owner:       "bborbe",
					Repo:        "repo",
					AuthorLogin: "alice",
					UpdatedAt:   libtime.DateTime(fixedNow),
				},
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  prs,
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha1",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(ghClient.GetPRDetailsCallCount()).To(Equal(1))
		})
	})

	Describe("buildFrontmatter fields", func() {
		It("includes required keys", func() {
			pr := pkg.PullRequest{
				Number:      5,
				Owner:       "bborbe",
				Repo:        "repo",
				Title:       "my title",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(fixedNow),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha1",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())

			Expect(createSender.SendCommandCallCount()).To(Equal(1))
			_, cmd := createSender.SendCommandArgsForCall(0)
			Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
			Expect(cmd.Frontmatter["assignee"]).To(Equal("pr-reviewer-agent"))
			Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
			Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
			Expect(cmd.Frontmatter["stage"]).To(Equal("dev"))
			Expect(cmd.Frontmatter["title"]).To(Equal("my title"))
			Expect(cmd.Frontmatter["task_identifier"]).NotTo(BeEmpty())
			Expect(cmd.Title).To(Equal("PR Review github - bborbe-repo - 5 - sha1 - my-title"))
		})
	})

	Describe("New commit (per-SHA spawn) — title contains SHA segment", func() {
		It("CreateTaskCommand title contains sha[:8] between PR number and slug", func() {
			pr := pkg.PullRequest{
				Number:      7,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}

			// Poll 1: SHA=sha-v1xx
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-v1xx",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)
			w := newTestWatcher(ghClient, createSender, cursorPath, startTime, fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}))
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())

			// Poll 2: SHA=sha-v2xx — new CreateTaskCommand, title contains "sha-v2xx"
			createSender2 := new(taskmocks.TaskCreateCommandSender)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha-v2xx",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender2.SendCommandReturns(nil)
			w2 := newTestWatcher(ghClient, createSender2, cursorPath, startTime, fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}))
			Expect(w2.Poll(ctx)).NotTo(HaveOccurred())

			Expect(createSender2.SendCommandCallCount()).To(Equal(1))
			_, cmd := createSender2.SendCommandArgsForCall(0)
			Expect(cmd.Title).To(ContainSubstring("sha-v2xx"))
			Expect(cmd.Frontmatter["ref"]).To(Equal("sha-v2xx"))
			Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
		})
	})

	Describe("Fail-closed on empty head SHA", func() {
		It("skips PR when HeadSHA is empty, no create published", func() {
			pr := pkg.PullRequest{
				Number:      88,
				Owner:       "bborbe",
				Repo:        "repo",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)

			w := newTestWatcher(
				ghClient,
				createSender,
				cursorPath,
				startTime,
				fakeMetrics,
				trust.NewAuthorAllowlist([]string{"alice"}),
			)
			Expect(w.Poll(ctx)).NotTo(HaveOccurred())
			Expect(createSender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Describe("Trust decisions", func() {
		var (
			pr pkg.PullRequest
		)

		BeforeEach(func() {
			pr = pkg.PullRequest{
				Number:      10,
				Owner:       "bborbe",
				Repo:        "repo",
				Title:       "some PR",
				HTMLURL:     "https://github.com/bborbe/repo/pull/10",
				AuthorLogin: "alice",
				UpdatedAt:   libtime.DateTime(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "sha1",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
		})

		Describe("Trusted-author new PR", func() {
			It("publishes CreateTaskCommand with planning/in_progress frontmatter", func() {
				createSender.SendCommandReturns(nil)
				w := newTestWatcher(
					ghClient,
					createSender,
					cursorPath,
					startTime,
					fakeMetrics,
					trust.NewAuthorAllowlist([]string{"alice"}),
				)
				Expect(w.Poll(ctx)).NotTo(HaveOccurred())
				Expect(createSender.SendCommandCallCount()).To(Equal(1))
				_, cmd := createSender.SendCommandArgsForCall(0)
				Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
				Expect(cmd.Frontmatter["assignee"]).To(Equal("pr-reviewer-agent"))
				Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
				Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
				Expect(cmd.Title).To(Equal("PR Review github - bborbe-repo - 10 - sha1 - some-pr"))
			})
		})

		Describe("Untrusted-author new PR", func() {
			It(
				"publishes CreateTaskCommand with human_review/todo frontmatter and untrusted body",
				func() {
					createSender.SendCommandReturns(nil)
					w := newTestWatcher(
						ghClient,
						createSender,
						cursorPath,
						startTime,
						fakeMetrics,
						trust.NewAuthorAllowlist([]string{"bob"}),
					)
					Expect(w.Poll(ctx)).NotTo(HaveOccurred())
					Expect(createSender.SendCommandCallCount()).To(Equal(1))
					_, cmd := createSender.SendCommandArgsForCall(0)
					Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
					Expect(cmd.Frontmatter["assignee"]).To(Equal(""))
					Expect(cmd.Frontmatter["phase"]).To(Equal("human_review"))
					Expect(cmd.Frontmatter["status"]).To(Equal("todo"))
					Expect(cmd.Body).To(ContainSubstring("alice"))
					Expect(cmd.Body).To(ContainSubstring("Untrusted author"))
					Expect(cmd.Body).To(ContainSubstring("phase: in_progress"))
					Expect(
						cmd.Title,
					).To(Equal("PR Review github - bborbe-repo - 10 - sha1 - some-pr"))
				},
			)
		})

		Describe("Untrusted-author new commit", func() {
			It(
				"publishes CreateTaskCommand with human_review/todo frontmatter for new SHA",
				func() {
					createSender.SendCommandReturns(nil)
					w := newTestWatcher(ghClient, createSender, cursorPath, startTime, fakeMetrics,
						trust.NewAuthorAllowlist([]string{"bob"}))
					Expect(w.Poll(ctx)).NotTo(HaveOccurred())
					Expect(createSender.SendCommandCallCount()).To(Equal(1))

					createSender2 := new(taskmocks.TaskCreateCommandSender)
					ghClient.GetPRDetailsReturns(
						pkg.PRDetails{
							HeadSHA:  "sha2",
							CloneURL: "https://github.com/owner/repo.git",
							BaseRef:  "master",
						},
						nil,
					)
					createSender2.SendCommandReturns(nil)
					w2 := newTestWatcher(
						ghClient,
						createSender2,
						cursorPath,
						startTime,
						fakeMetrics,
						trust.NewAuthorAllowlist([]string{"bob"}),
					)
					Expect(w2.Poll(ctx)).NotTo(HaveOccurred())
					Expect(createSender2.SendCommandCallCount()).To(Equal(1))
					_, cmd := createSender2.SendCommandArgsForCall(0)
					Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
					Expect(cmd.Frontmatter["assignee"]).To(Equal(""))
					Expect(cmd.Frontmatter["phase"]).To(Equal("human_review"))
					Expect(cmd.Frontmatter["status"]).To(Equal("todo"))
				},
			)
		})

		Describe("Trust check returns an error", func() {
			It(
				"skips the PR, no publish, Poll returns nil, cursor NOT advanced past failed PR",
				func() {
					fakeErr := errors.New("github rate limit")
					fakeTrust := new(mocks.Trust)
					fakeTrust.IsTrustedReturns(nil, fakeErr)
					w := newTestWatcher(
						ghClient,
						createSender,
						cursorPath,
						startTime,
						fakeMetrics,
						fakeTrust,
					)
					Expect(w.Poll(ctx)).NotTo(HaveOccurred())
					Expect(createSender.SendCommandCallCount()).To(Equal(0))
					taskIDStr := pkg.DeriveTaskID(pr.Owner, pr.Repo, pr.Number, "sha1").String()
					cursor, loadErr := pkg.LoadCursor(ctx, cursorPath, startTime)
					Expect(loadErr).NotTo(HaveOccurred())
					Expect(cursor.HeadSHAs).NotTo(HaveKey(taskIDStr))
				},
			)
		})

		Describe("PR with missing AuthorLogin (defensive)", func() {
			It("treats as untrusted and publishes human_review task", func() {
				pr.AuthorLogin = ""
				ghClient.SearchPRsReturns(pkg.SearchResult{
					PullRequests:  []pkg.PullRequest{pr},
					HasNextPage:   false,
					RateRemaining: 100,
				}, nil)
				createSender.SendCommandReturns(nil)
				w := newTestWatcher(
					ghClient,
					createSender,
					cursorPath,
					startTime,
					fakeMetrics,
					trust.NewAuthorAllowlist([]string{"alice"}),
				)
				Expect(w.Poll(ctx)).NotTo(HaveOccurred())
				Expect(createSender.SendCommandCallCount()).To(Equal(1))
				_, cmd := createSender.SendCommandArgsForCall(0)
				Expect(cmd.Frontmatter["task_type"]).To(Equal("pr-review"))
				Expect(cmd.Frontmatter["assignee"]).To(Equal(""))
				Expect(cmd.Frontmatter["phase"]).To(Equal("human_review"))
				Expect(cmd.Body).To(ContainSubstring("unknown"))
				Expect(cmd.Title).To(Equal("PR Review github - bborbe-repo - 10 - sha1 - some-pr"))
			})
		})
	})

	Describe("TaskPublisher contract", func() {
		It("calls publisher.PublishCreate with derived taskID for new (PR,SHA) pairs", func() {
			pr := pkg.PullRequest{
				Number:      42,
				Owner:       "bborbe",
				Repo:        "code-reviewer",
				Title:       "feat: new feature",
				HTMLURL:     "https://github.com/bborbe/maintainer/pull/42",
				AuthorLogin: "alice",
				IsDraft:     false,
				UpdatedAt:   libtime.DateTime(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
			}
			ghClient.SearchPRsReturns(pkg.SearchResult{
				PullRequests:  []pkg.PullRequest{pr},
				HasNextPage:   false,
				RateRemaining: 100,
			}, nil)
			ghClient.GetPRDetailsReturns(
				pkg.PRDetails{
					HeadSHA:  "abc123",
					CloneURL: "https://github.com/owner/repo.git",
					BaseRef:  "master",
				},
				nil,
			)
			createSender.SendCommandReturns(nil)

			fakePublisher := new(mocks.TaskPublisher)
			fakePublisher.PublishCreateReturns(true)

			w := pkg.NewWatcher(
				ghClient,
				fakePublisher,
				fakeMetrics,
				cursorPath,
				startTime,
				"bborbe",
				filter.TaskCreationFilters{
					filter.NewDraftFilter(),
					filter.NewBotAuthorFilter([]string{"dependabot[bot]"}),
				},
			)
			err := w.Poll(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(fakePublisher.PublishCreateCallCount()).To(Equal(1))

			ctxArg, prArg, taskIDStrArg, detailsArg := fakePublisher.PublishCreateArgsForCall(0)
			Expect(ctxArg).To(Equal(ctx))
			Expect(prArg.Number).To(Equal(pr.Number))
			Expect(prArg.Owner).To(Equal(pr.Owner))
			Expect(prArg.Repo).To(Equal(pr.Repo))
			Expect(taskIDStrArg).To(Equal(
				pkg.DeriveTaskID(pr.Owner, pr.Repo, pr.Number, "abc123").String()))
			Expect(detailsArg.HeadSHA).To(Equal("abc123"))
		})
	})
})

var _ = Describe("BuildCreateCommand", func() {
	makePR := func(login string) pkg.PullRequest {
		return pkg.PullRequest{
			Number:      1,
			Owner:       "owner",
			Repo:        "repo",
			Title:       "feat: add new feature",
			HTMLURL:     "https://github.com/owner/repo/pull/1",
			AuthorLogin: login,
			IsDraft:     false,
			UpdatedAt:   libtime.DateTime(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)),
		}
	}

	makeDetails := func() pkg.PRDetails {
		return pkg.PRDetails{
			HeadSHA:  "abc123",
			CloneURL: "https://github.com/owner/repo.git",
			BaseRef:  "master",
		}
	}

	It(
		"trusted author — sets phase=planning, status=in_progress, assignee=pr-reviewer-agent",
		func() {
			pr := makePR("trusted-user")
			details := makeDetails()
			trustResult := trust.NewResult(true, "author allowlist")
			taskIDStr := "00000000-0000-0000-0000-000000000001"

			cmd := pkg.BuildCreateCommand(
				pr,
				details,
				taskIDStr,
				"dev",
				80,
				60,
				"pr-reviewer",
				trustResult,
			)

			Expect(cmd.Title).NotTo(BeEmpty())
			Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
			Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
			Expect(cmd.Frontmatter["assignee"]).To(Equal("pr-reviewer-agent"))
			Expect(cmd.Body).To(ContainSubstring("feat: add new feature"))
		},
	)

	It("untrusted author — sets phase=human_review, status=todo, assignee=empty", func() {
		pr := makePR("unknown-user")
		details := makeDetails()
		trustResult := trust.NewResult(false, "author not in allowlist")
		taskIDStr := "00000000-0000-0000-0000-000000000002"

		cmd := pkg.BuildCreateCommand(
			pr,
			details,
			taskIDStr,
			"dev",
			80,
			60,
			"pr-reviewer",
			trustResult,
		)

		Expect(cmd.Title).NotTo(BeEmpty())
		Expect(cmd.Frontmatter["phase"]).To(Equal("human_review"))
		Expect(cmd.Frontmatter["status"]).To(Equal("todo"))
		Expect(cmd.Frontmatter["assignee"]).To(Equal(""))
		Expect(cmd.Body).To(ContainSubstring("Untrusted author"))
		Expect(cmd.Body).To(ContainSubstring("unknown-user"))
		Expect(cmd.Body).To(ContainSubstring("author not in allowlist"))
	})

	It("untrusted author with empty login — body contains (unknown)", func() {
		pr := makePR("")
		details := makeDetails()
		trustResult := trust.NewResult(false, "no author")
		taskIDStr := "00000000-0000-0000-0000-000000000003"

		cmd := pkg.BuildCreateCommand(
			pr,
			details,
			taskIDStr,
			"dev",
			80,
			60,
			"pr-reviewer",
			trustResult,
		)

		Expect(cmd.Body).To(ContainSubstring("(unknown)"))
	})

	It("title sanitizes special characters", func() {
		pr := makePR("trusted-user")
		pr.Title = "fix: handle /api?id=1 in :backend"
		details := makeDetails()
		trustResult := trust.NewResult(true, "author allowlist")
		taskIDStr := "00000000-0000-0000-0000-000000000004"

		cmd := pkg.BuildCreateCommand(
			pr,
			details,
			taskIDStr,
			"dev",
			80,
			60,
			"pr-reviewer",
			trustResult,
		)

		// Title must not contain slashes or colons that could break filename
		Expect(cmd.Title).NotTo(ContainSubstring("/"))
		Expect(cmd.Title).NotTo(ContainSubstring(":"))
	})

	It("respects maxTitleLen truncation", func() {
		pr := makePR("trusted-user")
		pr.Title = strings.Repeat("a", 200)
		details := makeDetails()
		trustResult := trust.NewResult(true, "author allowlist")
		taskIDStr := "00000000-0000-0000-0000-000000000005"

		cmd := pkg.BuildCreateCommand(
			pr,
			details,
			taskIDStr,
			"dev",
			80,
			30,
			"pr-reviewer",
			trustResult,
		) // maxTitleLen=30

		Expect(len(cmd.Title)).To(BeNumerically("<=", 30+len("-github-owner-repo-1-abc123")))
	})
})
