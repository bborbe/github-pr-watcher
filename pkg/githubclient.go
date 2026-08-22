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

	"github.com/bborbe/maintainer/maintainerconfig"
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
	// Labels holds the PR's label names (e.g. `override-review`). Populated
	// from the Search API's issue labels.
	Labels []string
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

	// Labels holds the PR's label names (e.g. `override-review`). Used to
	// route a labeled PR to the override task type.
	Labels []string
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

	// EnableAutoMerge arms GitHub-native auto-merge on a PR: when all merge
	// requirements (required status checks + required reviews per the repo
	// ruleset) are met, GitHub merges the PR automatically. Re-arming an
	// already-armed PR succeeds. Arming a PR that is ALREADY mergeable fails
	// with UNPROCESSABLE "Pull request is in clean status" — auto-merge only
	// applies while something still blocks the merge. Requires the
	// authenticating identity to hold Pull requests: Write on the repo.
	EnableAutoMerge(ctx context.Context, owner, repo string, number int) error

	// GetMaintainerConfig fetches and parses the repo's `.maintainer.yaml`
	// trust file via maintainerconfig. An absent file (404) returns the
	// zero-value config with nil error — matching the "opt-in, never
	// defaulted-on" trust-gate contract. Other errors are wrapped.
	GetMaintainerConfig(
		ctx context.Context,
		owner, repo string,
	) (maintainerconfig.MaintainerConfig, error)

	// ListPRFiles returns the PR's changed files with their patches, used by
	// the mechanical triviality classifier.
	ListPRFiles(ctx context.Context, owner, repo string, number int) ([]PRFile, error)

	// AddLabel adds a label to a PR (a no-op when already present). Requires
	// the authenticating identity to hold Pull requests: Write on the repo.
	AddLabel(ctx context.Context, owner, repo string, number int, label string) error
}

// PRFile is a single changed file in a PR with its patch, as returned by the
// pull-request files endpoint. Patch may be empty for large/binary files.
type PRFile struct {
	Filename string
	Patch    string
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
			Labels:      labelNames(issue.Labels),
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
		Labels:      labelNames(pr.Labels),
	}, nil
}

// EnableAutoMerge arms GitHub-native auto-merge on a PR.
//
// There is NO REST endpoint for this. GitHub exposes auto-merge only through
// the GraphQL `enablePullRequestAutoMerge` mutation; the plausible-looking
// PUT /repos/{owner}/{repo}/pulls/{number}/auto-merge returns 404 Not Found.
// go-github has no typed wrapper because there is no REST route to wrap.
//
// The mutation needs the PR's GraphQL node id, so this fetches the PR first.
// mergeMethod is pinned to MERGE to match the repo convention (bborbe repos
// are merge-commit-only: allow_squash_merge=false, allow_rebase_merge=false).
//
// GitHub answers a failed mutation with HTTP 200 and a non-empty `errors`
// array, so the response body must be inspected — a nil error from Do is not
// success. The common failure is UNPROCESSABLE "Pull request is in clean
// status": auto-merge can only be armed while something still blocks the
// merge. An already-mergeable PR cannot be armed, it can only be merged.
func (c *githubClient) EnableAutoMerge(
	ctx context.Context,
	owner, repo string,
	number int,
) error {
	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return errors.Wrapf(ctx, err, "get pull request %s/%s#%d", owner, repo, number)
	}
	nodeID := pr.GetNodeID()
	if nodeID == "" {
		return errors.Errorf(ctx, "missing node id for %s/%s#%d", owner, repo, number)
	}

	body := struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}{
		Query: `mutation($id:ID!){enablePullRequestAutoMerge(input:{pullRequestId:$id,mergeMethod:MERGE}){pullRequest{number}}}`,
		Variables: map[string]any{
			"id": nodeID,
		},
	}
	req, err := c.client.NewRequest(http.MethodPost, "graphql", body)
	if err != nil {
		return errors.Wrapf(ctx, err, "enable auto merge %s/%s#%d", owner, repo, number)
	}
	var result struct {
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.client.Do(ctx, req, &result); err != nil {
		return errors.Wrapf(ctx, err, "enable auto merge %s/%s#%d", owner, repo, number)
	}
	if len(result.Errors) > 0 {
		return errors.Errorf(
			ctx,
			"enable auto merge %s/%s#%d: %s %s",
			owner, repo, number,
			result.Errors[0].Type,
			result.Errors[0].Message,
		)
	}
	return nil
}

// GetMaintainerConfig fetches and parses the repo's `.maintainer.yaml` trust
// file. A 404 (file absent) returns the zero-value config with nil error —
// the repo simply has not opted in. Any other failure is wrapped.
func (c *githubClient) GetMaintainerConfig(
	ctx context.Context,
	owner, repo string,
) (maintainerconfig.MaintainerConfig, error) {
	content, _, resp, err := c.client.Repositories.GetContents(
		ctx,
		owner,
		repo,
		".maintainer.yaml",
		nil,
	)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return maintainerconfig.MaintainerConfig{}, nil
		}
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx,
			err,
			"get .maintainer.yaml %s/%s",
			owner,
			repo,
		)
	}
	raw, err := content.GetContent()
	if err != nil {
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx,
			err,
			"decode .maintainer.yaml %s/%s",
			owner,
			repo,
		)
	}
	cfg, err := maintainerconfig.Parse(ctx, []byte(raw))
	if err != nil {
		return maintainerconfig.MaintainerConfig{}, errors.Wrapf(
			ctx,
			err,
			"parse .maintainer.yaml %s/%s",
			owner,
			repo,
		)
	}
	return cfg, nil
}

// ListPRFiles pages through the PR's changed files. Pagination is followed to
// exhaustion (GitHub caps at 100 per page; large PRs span multiple pages).
func (c *githubClient) ListPRFiles(
	ctx context.Context,
	owner, repo string,
	number int,
) ([]PRFile, error) {
	var files []PRFile
	opts := &gogithub.ListOptions{PerPage: 100}
	for {
		page, resp, err := c.client.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, errors.Wrapf(
				ctx,
				err,
				"list pr files %s/%s#%d",
				owner,
				repo,
				number,
			)
		}
		for _, f := range page {
			files = append(files, PRFile{Filename: f.GetFilename(), Patch: f.GetPatch()})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return files, nil
}

// AddLabel adds a label to a PR. Adding an already-present label is a no-op on
// the GitHub side; the call still succeeds.
func (c *githubClient) AddLabel(
	ctx context.Context,
	owner, repo string,
	number int,
	label string,
) error {
	_, _, err := c.client.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{label})
	if err != nil {
		return errors.Wrapf(
			ctx,
			err,
			"add label %q %s/%s#%d",
			label,
			owner,
			repo,
			number,
		)
	}
	return nil
}

// labelNames maps GitHub label objects to their names, skipping any with an
// empty name. Returns nil for an empty input so callers see no labels.
func labelNames(labels []*gogithub.Label) []string {
	if len(labels) == 0 {
		return nil
	}
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		if name := label.GetName(); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// parseOwnerRepo extracts owner and repo from a GitHub API repository URL.
// Input format: https://api.github.com/repos/{owner}/{repo}
func parseOwnerRepo(repoURL string) (owner, repo string) {
	dir, repoName := path.Split(repoURL)
	_, ownerName := path.Split(path.Clean(dir))
	return ownerName, repoName
}
