// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	stderrors "errors"
	"net/http"
	"net/http/httptest"

	"github.com/bborbe/github-pr-watcher/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	githubAPITestService = "github-pr-watcher"
	githubAPITestCluster = "test-cluster"
)

func githubAPICallsValue(credential, bucket, outcome string) float64 {
	return testutil.ToFloat64(pkg.GitHubAPICallsTotalForTest().WithLabelValues(
		githubAPITestService, credential, bucket, outcome, githubAPITestCluster,
	))
}

func githubAPIRateLimitedValue(credential, bucket, class string) float64 {
	return testutil.ToFloat64(pkg.GitHubAPIRateLimitedTotalForTest().WithLabelValues(
		githubAPITestService, credential, bucket, class, githubAPITestCluster,
	))
}

func mustGitHubAPIRequest(rawURL string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	Expect(err).NotTo(HaveOccurred())
	return req
}

var _ = Describe("GitHubAPIBucket", func() {
	DescribeTable(
		"derives the bucket from the request path only",
		func(req *http.Request, want string) {
			Expect(pkg.GitHubAPIBucket(req)).To(Equal(want))
		},
		Entry(
			"search prefix",
			mustGitHubAPIRequest("https://api.github.com/search/issues"),
			"search",
		),
		Entry(
			"enterprise search prefix",
			mustGitHubAPIRequest("https://ghe.example.com/api/v3/search/issues"),
			"search",
		),
		Entry("graphql exact", mustGitHubAPIRequest("https://api.github.com/graphql"), "graphql"),
		Entry(
			"enterprise graphql exact",
			mustGitHubAPIRequest("https://ghe.example.com/api/graphql"),
			"graphql",
		),
		Entry(
			"core pulls path",
			mustGitHubAPIRequest("https://api.github.com/repos/o/r/pulls"),
			"core",
		),
		Entry(
			"core rate_limit path",
			mustGitHubAPIRequest("https://api.github.com/rate_limit"),
			"core",
		),
		Entry(
			"search without trailing slash is core",
			mustGitHubAPIRequest("https://api.github.com/search"),
			"core",
		),
		Entry(
			"search inside a path is core",
			mustGitHubAPIRequest("https://api.github.com/repos/o/search/x"),
			"core",
		),
		Entry(
			"graphql with a suffix is core",
			mustGitHubAPIRequest("https://api.github.com/graphql/x"),
			"core",
		),
		Entry(
			"query string does not change the bucket",
			mustGitHubAPIRequest("https://api.github.com/search/issues?q=x"),
			"search",
		),
		Entry("nil request is core", nil, "core"),
	)
})

var _ = Describe("GitHub API metrics", func() {
	It("attributes interleaved calls by request path, not response headers", func() {
		const credential = "test-interleave"
		apiMetrics := pkg.NewGitHubAPIMetrics(credential, githubAPITestCluster)

		searchCalls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/search/issues":
				searchCalls++
				if searchCalls > 1 {
					w.Header().Set("X-RateLimit-Resource", "search")
				}
			case "/repos/o/r/pulls":
				w.Header().Set("X-RateLimit-Resource", "core")
			}
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(srv.Close)

		client := srv.Client()
		pkg.NewGitHubClient(client, apiMetrics)

		get := func(path string) {
			resp, err := client.Get(srv.URL + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
		}
		get("/search/issues")
		get("/repos/o/r/pulls")
		get("/search/issues")

		Expect(githubAPICallsValue(credential, "search", "success")).To(Equal(float64(2)))
		Expect(githubAPICallsValue(credential, "core", "success")).To(Equal(float64(1)))
		Expect(githubAPICallsValue(credential, "graphql", "success")).To(Equal(float64(0)))
		for _, bucket := range []string{"search", "graphql", "core"} {
			Expect(githubAPICallsValue(credential, bucket, "error")).To(Equal(float64(0)))
		}
	})

	It("ignores an X-RateLimit-Resource header that contradicts the request path", func() {
		const credential = "test-contradict"
		apiMetrics := pkg.NewGitHubAPIMetrics(credential, githubAPITestCluster)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/search/issues":
				w.Header().Set("X-RateLimit-Resource", "core")
			case "/repos/o/r/pulls":
				w.Header().Set("X-RateLimit-Resource", "search")
			}
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(srv.Close)

		client := srv.Client()
		pkg.NewGitHubClient(client, apiMetrics)

		get := func(path string) {
			resp, err := client.Get(srv.URL + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
		}
		get("/search/issues")
		get("/repos/o/r/pulls")
		get("/search/issues")

		Expect(githubAPICallsValue(credential, "search", "success")).To(Equal(float64(2)))
		Expect(githubAPICallsValue(credential, "core", "success")).To(Equal(float64(1)))
	})

	DescribeTable(
		"counts status and rate-limit outcomes",
		func(status int, path string, headers map[string]string, credential, bucket string, wantRateLimited bool) {
			apiMetrics := pkg.NewGitHubAPIMetrics(credential, githubAPITestCluster)
			srv := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					for k, v := range headers {
						w.Header().Set(k, v)
					}
					w.WriteHeader(status)
				}),
			)
			DeferCleanup(srv.Close)

			client := srv.Client()
			pkg.NewGitHubClient(client, apiMetrics)

			resp, err := client.Get(srv.URL + path)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())

			wantRateLimitedValue := float64(0)
			if wantRateLimited {
				wantRateLimitedValue = 1
			}
			Expect(
				githubAPIRateLimitedValue(credential, bucket, "unclassified"),
			).To(Equal(wantRateLimitedValue))

			if status < 400 {
				Expect(githubAPICallsValue(credential, bucket, "success")).To(Equal(float64(1)))
				Expect(githubAPICallsValue(credential, bucket, "error")).To(Equal(float64(0)))
			} else {
				Expect(githubAPICallsValue(credential, bucket, "error")).To(Equal(float64(1)))
				Expect(githubAPICallsValue(credential, bucket, "success")).To(Equal(float64(0)))
			}
		},
		Entry(
			"429 is rate limited",
			http.StatusTooManyRequests,
			"/repos/o/r/pulls",
			nil,
			"test-status-429",
			"core",
			true,
		),
		Entry("403 with exhausted quota is rate limited", http.StatusForbidden, "/repos/o/r/pulls",
			map[string]string{"X-RateLimit-Remaining": "0"}, "test-status-403-quota", "core", true),
		Entry("403 with retry-after is rate limited", http.StatusForbidden, "/search/issues",
			map[string]string{"Retry-After": "60"}, "test-status-403-retry", "search", true),
		Entry(
			"403 with remaining quota is not rate limited",
			http.StatusForbidden,
			"/repos/o/r/pulls",
			map[string]string{
				"X-RateLimit-Remaining": "4999",
			},
			"test-status-403-ok",
			"core",
			false,
		),
		Entry(
			"404 is an error but not rate limited",
			http.StatusNotFound,
			"/repos/o/r/pulls",
			nil,
			"test-status-404",
			"core",
			false,
		),
		Entry(
			"200 is success",
			http.StatusOK,
			"/repos/o/r/pulls",
			nil,
			"test-status-200",
			"core",
			false,
		),
	)

	It("counts a transport error as an error call and never as rate limited", func() {
		const credential = "test-transport-error"
		apiMetrics := pkg.NewGitHubAPIMetrics(credential, githubAPITestCluster)

		client := &http.Client{Transport: failingRoundTripper{}}
		pkg.NewGitHubClient(client, apiMetrics)

		resp, err := client.Get("https://api.github.com/search/issues")
		Expect(err).To(HaveOccurred())
		Expect(resp).To(BeNil())

		Expect(githubAPICallsValue(credential, "search", "error")).To(Equal(float64(1)))
		Expect(githubAPICallsValue(credential, "search", "success")).To(Equal(float64(0)))
		Expect(
			githubAPIRateLimitedValue(credential, "search", "unclassified"),
		).To(Equal(float64(0)))
	})

	It("registers both counters and pre-initialises every series", func() {
		beforeCalls, err := testutil.GatherAndCount(
			prometheus.DefaultGatherer, "github_api_calls_total",
		)
		Expect(err).NotTo(HaveOccurred())
		beforeRateLimited, err := testutil.GatherAndCount(
			prometheus.DefaultGatherer, "github_api_rate_limited_total",
		)
		Expect(err).NotTo(HaveOccurred())

		const credential = "test-preinit"
		pkg.NewGitHubAPIMetrics(credential, githubAPITestCluster)

		afterCalls, err := testutil.GatherAndCount(
			prometheus.DefaultGatherer, "github_api_calls_total",
		)
		Expect(err).NotTo(HaveOccurred())
		afterRateLimited, err := testutil.GatherAndCount(
			prometheus.DefaultGatherer, "github_api_rate_limited_total",
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(afterCalls).To(Equal(beforeCalls + 6))
		Expect(afterRateLimited).To(Equal(beforeRateLimited + 3))

		for _, bucket := range []string{"search", "graphql", "core"} {
			for _, outcome := range []string{"success", "error"} {
				Expect(githubAPICallsValue(credential, bucket, outcome)).To(Equal(float64(0)))
			}
			Expect(
				githubAPIRateLimitedValue(credential, bucket, "unclassified"),
			).To(Equal(float64(0)))
		}
	})
})

// failingRoundTripper returns a transport error for every request.
type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, stderrors.New("connection refused")
}
