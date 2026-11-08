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

	"github.com/bborbe/github-pr-watcher/pkg"
	gogithub "github.com/google/go-github/v62/github"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newCommitStateServer serves the two endpoints GetCommitState reads. checkRuns
// is called with the 1-based page number.
func newCommitStateServer(
	statusBody string,
	checkRuns func(page int) (int, string),
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/bborbe/repo/commits/deadbeef/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(statusBody))
		case "/repos/bborbe/repo/commits/deadbeef/check-runs":
			page := 1
			if raw := r.URL.Query().Get("page"); raw != "" {
				page, _ = strconv.Atoi(raw)
			}
			code, body := checkRuns(page)
			if code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newCommitStateClient(server *httptest.Server) pkg.GitHubClient {
	client := gogithub.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	Expect(err).To(BeNil())
	client.BaseURL = baseURL
	return pkg.NewForTest(client)
}

var _ = Describe("GetCommitState", func() {
	var server *httptest.Server

	AfterEach(func() {
		if server != nil {
			server.Close()
			server = nil
		}
	})

	It("returns an error when the check-runs read fails", func() {
		server = newCommitStateServer(
			`{"state":"success","total_count":1}`,
			func(page int) (int, string) {
				return http.StatusInternalServerError, ""
			},
		)
		client := newCommitStateClient(server)
		_, err := client.GetCommitState(context.Background(), "bborbe", "repo", "deadbeef")
		Expect(err).NotTo(BeNil())
	})

	It("returns an error when the combined-status read fails", func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		client := newCommitStateClient(server)
		_, err := client.GetCommitState(context.Background(), "bborbe", "repo", "deadbeef")
		Expect(err).NotTo(BeNil())
	})

	It("returns an error when the context is cancelled", func() {
		server = newCommitStateServer(
			`{"state":"success","total_count":1}`,
			func(page int) (int, string) {
				return http.StatusOK, `{"total_count":0,"check_runs":[]}`
			},
		)
		client := newCommitStateClient(server)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.GetCommitState(ctx, "bborbe", "repo", "deadbeef")
		Expect(err).NotTo(BeNil())
	})
})

var _ = Describe("GetCommitState pagination", func() {
	It("reads a failing run that only appears on page 2", func() {
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/bborbe/repo/commits/deadbeef/status":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"state":"success","total_count":1}`))
			case "/repos/bborbe/repo/commits/deadbeef/check-runs":
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("page") == "2" {
					_, _ = w.Write([]byte(`{"total_count":2,"check_runs":[
						{"status":"completed","conclusion":"failure"}]}`))
					return
				}
				next := fmt.Sprintf(
					`<%s/repos/bborbe/repo/commits/deadbeef/check-runs?page=2>; rel="next"`,
					server.URL,
				)
				w.Header().Set("Link", next)
				_, _ = w.Write([]byte(`{"total_count":2,"check_runs":[
					{"status":"completed","conclusion":"success"}]}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		client := newCommitStateClient(server)
		state, err := client.GetCommitState(
			context.Background(),
			"bborbe",
			"repo",
			"deadbeef",
		)
		Expect(err).To(BeNil())
		Expect(state).To(Equal("failure"))
	})
})
