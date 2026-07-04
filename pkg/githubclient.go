// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	gogithub "github.com/google/go-github/v62/github"
)

// PullRequest holds the fields the watcher needs from a GitHub PR.
type PullRequest struct {
	GlobalID    int64
	Number      int
	Owner       string
	Repo        string
	Title       string
	HTMLURL     string
	HeadSHA     string
	AuthorLogin string
	IsDraft     bool
	UpdatedAt   libtime.DateTime
}

// SearchResult is the result of a single paginated search call.
type SearchResult struct {
	PullRequests  []PullRequest
	HasNextPage   bool
	NextPage      int
	RateRemaining int
	RateResetAt   libtime.DateTime
}

// PRDetails holds the per-PR fields the watcher needs to materialize a task
// the execution phase can act on. The Search API does not expose any of
// these; they require a follow-up PullRequests.Get call.
type PRDetails struct {
	// HeadSHA is the commit hash of the PR's head branch. Used for force-push
	// detection and as the `ref` the agent checks out for review.
	HeadSHA string

	// CloneURL is the HTTPS clone URL of the head repo (e.g.
	// `https://github.com/owner/repo.git`). Used as the `clone_url` the
	// agent's execution phase passes to git clone.
	CloneURL string

	// BaseRef is the base branch name (e.g. `master`, `main`). Used as
	// the `base_ref` the execution phase diffs against.
	BaseRef string

	// AuthorLogin is the GitHub author login; empty for deleted accounts.
	AuthorLogin string

	// Title is the PR title.
	Title string

	// IsDraft indicates whether the PR is a draft.
	IsDraft bool

	// UpdatedAt is the PR last-updated timestamp; required for AgeFilter.
	UpdatedAt libtime.DateTime
}

//counterfeiter:generate -o ../mocks/github_client.go --fake-name GitHubClient . GitHubClient

// GitHubClient abstracts the GitHub API calls.
type GitHubClient interface {
	// SearchPRs issues a GitHub Search query for open PRs updated since cursor.
	// page=1 for the first call; use SearchResult.NextPage for subsequent calls.
	// PullRequest.HeadSHA in the result is empty — call GetPRDetails to fetch it.
	SearchPRs(
		ctx context.Context,
		scope string,
		since libtime.DateTime,
		page int,
	) (SearchResult, error)

	// GetPRDetails fetches the head SHA, clone URL, and base ref for a single PR.
	// The Search API does NOT return any of these, so the poll loop must call
	// this for every PR before publishing a task command.
	GetPRDetails(ctx context.Context, owner, repo string, number int) (PRDetails, error)
}

// NewGitHubClient returns a GitHubClient backed by the real GitHub API.
// The httpClient must already carry authentication (App auth via
// lib/githubapp.NewClient).
func NewGitHubClient(httpClient *http.Client) GitHubClient {
	return &githubClient{
		client: gogithub.NewClient(httpClient),
	}
}

type githubClient struct {
	client *gogithub.Client
}

func (c *githubClient) SearchPRs(
	ctx context.Context,
	scope string,
	since libtime.DateTime,
	page int,
) (SearchResult, error) {
	query := fmt.Sprintf(
		"is:pr is:open archived:false user:%s updated:>=%s",
		scope,
		since.Format(time.RFC3339),
	)
	opts := &gogithub.SearchOptions{
		ListOptions: gogithub.ListOptions{
			Page:    page,
			PerPage: 100,
		},
	}

	result, resp, err := c.client.Search.Issues(ctx, query, opts)
	if err != nil {
		return SearchResult{}, errors.Wrapf(ctx, err, "search github prs scope=%s", scope)
	}

	prs := make([]PullRequest, 0, len(result.Issues))
	for _, issue := range result.Issues {
		repoURL := issue.GetRepositoryURL()
		owner, repo := parseOwnerRepo(repoURL)
		prs = append(prs, PullRequest{
			GlobalID:    issue.GetID(),
			Number:      issue.GetNumber(),
			Owner:       owner,
			Repo:        repo,
			Title:       issue.GetTitle(),
			HTMLURL:     issue.GetHTMLURL(),
			HeadSHA:     "",
			AuthorLogin: issue.GetUser().GetLogin(),
			IsDraft:     issue.GetDraft(),
			UpdatedAt:   libtime.DateTime(issue.GetUpdatedAt().Time),
		})
	}

	return SearchResult{
		PullRequests:  prs,
		HasNextPage:   resp.NextPage > 0,
		NextPage:      resp.NextPage,
		RateRemaining: resp.Rate.Remaining,
		RateResetAt:   libtime.DateTime(resp.Rate.Reset.Time),
	}, nil
}

func (c *githubClient) GetPRDetails(
	ctx context.Context,
	owner, repo string,
	number int,
) (PRDetails, error) {
	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return PRDetails{}, errors.Wrapf(
			ctx,
			err,
			"get pull request %s/%s#%d",
			owner,
			repo,
			number,
		)
	}
	return PRDetails{
		HeadSHA:     pr.GetHead().GetSHA(),
		CloneURL:    pr.GetHead().GetRepo().GetCloneURL(),
		BaseRef:     pr.GetBase().GetRef(),
		AuthorLogin: pr.GetUser().GetLogin(),
		Title:       pr.GetTitle(),
		IsDraft:     pr.GetDraft(),
		UpdatedAt:   libtime.DateTime(pr.GetUpdatedAt().Time),
	}, nil
}

// parseOwnerRepo extracts owner and repo from a GitHub API repository URL.
// Input format: https://api.github.com/repos/{owner}/{repo}
func parseOwnerRepo(repoURL string) (owner, repo string) {
	dir, repoName := path.Split(repoURL)
	_, ownerName := path.Split(path.Clean(dir))
	return ownerName, repoName
}
