// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import gogithub "github.com/google/go-github/v62/github"

// NewForTest creates a GitHubClient backed by the provided go-github client.
// Used only in tests to inject a custom client pointing at httptest.Server.
func NewForTest(c *gogithub.Client) GitHubClient {
	return &githubClient{client: c}
}
