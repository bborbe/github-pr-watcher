// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"net/http"
	"strings"
)

// Rate-limit bucket values reported on the github_api_* counters.
const (
	githubAPIBucketSearch  = "search"
	githubAPIBucketGraphQL = "graphql"
	githubAPIBucketCore    = "core"
)

// Call outcome values reported on github_api_calls_total.
const (
	githubAPIOutcomeSuccess = "success"
	githubAPIOutcomeError   = "error"
)

// githubAPIRateLimitClassUnclassified is the only rate-limit class emitted
// until a classification rule distinguishes primary from secondary limiting.
const githubAPIRateLimitClassUnclassified = "unclassified"

// githubAPIServiceName identifies this watcher as the origin of the
// cross-service github_api_* counters.
const githubAPIServiceName = "github-pr-watcher"

// GitHubAPIBucket returns the GitHub rate-limit bucket a request spends,
// derived from the request path only: /search/ and /api/v3/search/ are
// "search", /graphql and /api/graphql are "graphql", everything else is
// "core". A nil request or URL is "core".
//
// The bucket must never be read from a response header. X-RateLimit-Resource
// belongs to the response and arrives only after the call, so deriving the
// bucket from it (or from state left by a previous response) misattributes
// interleaved search and core calls to whichever bucket answered last.
func GitHubAPIBucket(req *http.Request) string {
	if req == nil || req.URL == nil {
		return githubAPIBucketCore
	}
	path := req.URL.Path
	switch {
	case strings.HasPrefix(path, "/search/"), strings.HasPrefix(path, "/api/v3/search/"):
		return githubAPIBucketSearch
	case path == "/graphql", path == "/api/graphql":
		return githubAPIBucketGraphQL
	default:
		return githubAPIBucketCore
	}
}

//counterfeiter:generate -o ../mocks/github_api_metrics.go --fake-name GitHubAPIMetrics . GitHubAPIMetrics

// GitHubAPIMetrics records per-caller GitHub API counters. The service,
// credential and cluster labels are bound at construction.
type GitHubAPIMetrics interface {
	// IncGitHubAPICall counts one round trip. outcome: "success", "error".
	IncGitHubAPICall(bucket string, outcome string)
	// IncGitHubAPIRateLimited counts one rate-limited response. class: "unclassified".
	IncGitHubAPIRateLimited(bucket string, class string)
}

type githubAPIMetrics struct {
	credential string
	cluster    string
}

// NewGitHubAPIMetrics returns a GitHubAPIMetrics that stamps every series with
// the constant service name, the given credential and the given cluster.
// credential is the GitHub App id as a decimal string; it must never be a
// token or any other secret. Every bounded label combination is
// pre-initialised to zero so alert rules see a flat zero from startup rather
// than missing data.
func NewGitHubAPIMetrics(credential string, cluster string) GitHubAPIMetrics {
	m := &githubAPIMetrics{credential: credential, cluster: cluster}
	for _, bucket := range []string{
		githubAPIBucketSearch,
		githubAPIBucketGraphQL,
		githubAPIBucketCore,
	} {
		for _, outcome := range []string{githubAPIOutcomeSuccess, githubAPIOutcomeError} {
			githubAPICallsTotal.WithLabelValues(
				githubAPIServiceName,
				credential,
				bucket,
				outcome,
				cluster,
			).Add(0)
		}
		githubAPIRateLimitedTotal.WithLabelValues(
			githubAPIServiceName,
			credential,
			bucket,
			githubAPIRateLimitClassUnclassified,
			cluster,
		).Add(0)
	}
	return m
}

func (m *githubAPIMetrics) IncGitHubAPICall(bucket string, outcome string) {
	githubAPICallsTotal.WithLabelValues(
		githubAPIServiceName,
		m.credential,
		bucket,
		outcome,
		m.cluster,
	).Inc()
}

func (m *githubAPIMetrics) IncGitHubAPIRateLimited(bucket string, class string) {
	githubAPIRateLimitedTotal.WithLabelValues(
		githubAPIServiceName,
		m.credential,
		bucket,
		class,
		m.cluster,
	).Inc()
}
