// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"time"

	"github.com/bborbe/github-pr-watcher/pkg"
	libtime "github.com/bborbe/time"
	gogithub "github.com/google/go-github/v62/github"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var fixedNow = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

var _ = Describe("pkg.GitHubClient", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() {
		cancel()
	})

	buildClient := func(server *httptest.Server) pkg.GitHubClient {
		ghc := gogithub.NewClient(server.Client())
		baseURL, _ := url.Parse(server.URL + "/")
		ghc.BaseURL = baseURL
		return pkg.NewForTest(ghc)
	}

	Describe("SearchPRs", func() {
		Context("single page, two PRs, no next page", func() {
			It("returns both PRs with correct fields", func() {
				resetAt := fixedNow.Add(time.Hour).Unix()
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.URL.Path).To(Equal("/search/issues"))
						w.Header().Set("X-RateLimit-Remaining", "4999")
						w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{
						"total_count": 2,
						"incomplete_results": false,
						"items": [
							{
								"id": 1001,
								"number": 42,
								"title": "Fix bug",
								"html_url": "https://github.com/owner/repo/pull/42",
								"repository_url": "https://api.github.com/repos/owner/repo",
								"user": {"login": "alice"},
								"draft": false,
								"updated_at": "2026-01-01T00:00:00Z"
							},
							{
								"id": 1002,
								"number": 43,
								"title": "Add feature",
								"html_url": "https://github.com/owner/repo/pull/43",
								"repository_url": "https://api.github.com/repos/owner/repo",
								"user": {"login": "bob"},
								"draft": true,
								"updated_at": "2026-01-02T00:00:00Z"
							}
						]
					}`)
					}),
				)
				defer server.Close()

				client := buildClient(server)
				result, err := client.SearchPRs(
					ctx,
					"owner",
					libtime.DateTime(fixedNow.Add(-24*time.Hour)),
					1,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.PullRequests).To(HaveLen(2))
				Expect(result.HasNextPage).To(BeFalse())
				Expect(result.RateRemaining).To(Equal(4999))

				pr := result.PullRequests[0]
				Expect(pr.GlobalID).To(Equal(int64(1001)))
				Expect(pr.Number).To(Equal(42))
				Expect(pr.Owner).To(Equal("owner"))
				Expect(pr.Repo).To(Equal("repo"))
				Expect(pr.Title).To(Equal("Fix bug"))
				Expect(pr.HTMLURL).To(Equal("https://github.com/owner/repo/pull/42"))
				Expect(pr.HeadSHA).To(Equal(""))
				Expect(pr.AuthorLogin).To(Equal("alice"))
				Expect(pr.IsDraft).To(BeFalse())

				pr2 := result.PullRequests[1]
				Expect(pr2.IsDraft).To(BeTrue())
				Expect(pr2.AuthorLogin).To(Equal("bob"))
			})
		})

		Context("pagination", func() {
			It("returns HasNextPage=true and correct NextPage for first page", func() {
				callCount := 0
				var serverURL string
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						callCount++
						w.Header().Set("X-RateLimit-Remaining", "100")
						w.Header().
							Set("X-RateLimit-Reset", strconv.FormatInt(fixedNow.Add(time.Hour).Unix(), 10))
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Query().Get("page") == "2" {
							fmt.Fprintf(w, `{
							"total_count": 2,
							"incomplete_results": false,
							"items": [
								{
									"id": 2002,
									"number": 2,
									"title": "PR 2",
									"html_url": "https://github.com/org/r/pull/2",
									"repository_url": "https://api.github.com/repos/org/r",
									"user": {"login": "carol"},
									"draft": false,
									"updated_at": "2026-02-01T00:00:00Z"
								}
							]
						}`)
						} else {
							w.Header().Set("Link", fmt.Sprintf(`<%s/search/issues?page=2>; rel="next"`, serverURL))
							fmt.Fprintf(w, `{
							"total_count": 2,
							"incomplete_results": false,
							"items": [
								{
									"id": 2001,
									"number": 1,
									"title": "PR 1",
									"html_url": "https://github.com/org/r/pull/1",
									"repository_url": "https://api.github.com/repos/org/r",
									"user": {"login": "dave"},
									"draft": false,
									"updated_at": "2026-01-15T00:00:00Z"
								}
							]
						}`)
						}
					}),
				)
				serverURL = server.URL
				defer server.Close()

				client := buildClient(server)

				result1, err := client.SearchPRs(
					ctx,
					"org",
					libtime.DateTime(fixedNow.Add(-24*time.Hour)),
					1,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(result1.HasNextPage).To(BeTrue())
				Expect(result1.NextPage).To(Equal(2))
				Expect(result1.PullRequests).To(HaveLen(1))

				result2, err := client.SearchPRs(
					ctx,
					"org",
					libtime.DateTime(fixedNow.Add(-24*time.Hour)),
					result1.NextPage,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(result2.HasNextPage).To(BeFalse())
				Expect(result2.PullRequests).To(HaveLen(1))
				Expect(callCount).To(Equal(2))
			})
		})

		Context("rate limit fields", func() {
			It("populates RateRemaining and RateResetAt correctly", func() {
				resetTime := fixedNow.Add(30 * time.Minute).Truncate(time.Second)
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("X-RateLimit-Remaining", "5")
						w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"total_count":0,"incomplete_results":false,"items":[]}`)
					}),
				)
				defer server.Close()

				client := buildClient(server)
				result, err := client.SearchPRs(
					ctx,
					"org",
					libtime.DateTime(fixedNow.Add(-time.Hour)),
					1,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RateRemaining).To(Equal(5))
				Expect(result.RateResetAt.Unix()).To(Equal(resetTime.Unix()))
			})
		})

		Context("GitHub API error (HTTP 401)", func() {
			It("returns a non-nil error", func() {
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						fmt.Fprintf(
							w,
							`{"message":"Bad credentials","documentation_url":"https://docs.github.com"}`,
						)
					}),
				)
				defer server.Close()

				client := buildClient(server)
				_, err := client.SearchPRs(
					ctx,
					"org",
					libtime.DateTime(fixedNow.Add(-time.Hour)),
					1,
				)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("context cancellation", func() {
			It("returns a non-nil error when context is cancelled", func() {
				server := httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// slow response — ctx will be cancelled before this runs in practice
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"total_count":0,"incomplete_results":false,"items":[]}`)
					}),
				)
				defer server.Close()

				cancelledCtx, cancelFn := context.WithCancel(ctx)
				cancelFn()

				client := buildClient(server)
				_, err := client.SearchPRs(
					cancelledCtx,
					"org",
					libtime.DateTime(fixedNow.Add(-time.Hour)),
					1,
				)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("GetPRDetails", func() {
		It("returns head SHA, clone URL, and base ref for a PR", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/repos/owner/repo/pulls/42"))
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{
					"number": 42,
					"head": {
						"sha": "abc123def456abc123def456abc123def456abc1",
						"repo": {
							"clone_url": "https://github.com/owner/repo.git"
						}
					},
					"base": {
						"ref": "master"
					}
				}`)
				}),
			)
			defer server.Close()

			client := buildClient(server)
			details, err := client.GetPRDetails(ctx, "owner", "repo", 42)
			Expect(err).NotTo(HaveOccurred())
			Expect(details.HeadSHA).To(Equal("abc123def456abc123def456abc123def456abc1"))
			Expect(details.CloneURL).To(Equal("https://github.com/owner/repo.git"))
			Expect(details.BaseRef).To(Equal("master"))
		})

		It("returns an error on HTTP failure", func() {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprintf(w, `{"message":"Not Found"}`)
				}),
			)
			defer server.Close()

			client := buildClient(server)
			_, err := client.GetPRDetails(ctx, "owner", "repo", 99)
			Expect(err).To(HaveOccurred())
		})
	})
})
