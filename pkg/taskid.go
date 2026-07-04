// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"

	"github.com/google/uuid"
)

// prWatcherNamespace is the fixed v5 UUID namespace for all watcher-derived task identifiers.
// This value is a constant — changing it invalidates all existing task identifiers.
var prWatcherNamespace = uuid.MustParse("7d4b3e5f-8a21-4c9d-b036-2e5f7a8c1d0e")

// DeriveTaskID returns a deterministic task identifier for a (PR, SHA) pair.
// Input: "<owner>/<repo>#<number>@<sha>", e.g. "bborbe/maintainer#42@abc123...".
// The full SHA is used (not truncated) to keep the dedup keyspace collision-free.
func DeriveTaskID(owner, repo string, number int, sha string) uuid.UUID {
	key := fmt.Sprintf("%s/%s#%d@%s", owner, repo, number, sha)
	return uuid.NewSHA1(prWatcherNamespace, []byte(key))
}

// DeriveTaskIDForce returns a salted task identifier for a (PR, SHA) pair
// plus an extra nonce. Used when an operator explicitly requests a forced
// re-review (HTTP /trigger?force=true) so the executor can publish a
// CreateTaskCommand with a TaskIdentifier that the controller has not
// already seen — bypassing the dedup-skip in the agent controller.
//
// For the same (owner, repo, number, sha) the result is always different
// from DeriveTaskID(...): the key includes the nonce segment "<nonce>",
// e.g. "bborbe/maintainer#42@abc123...!1700000000000000000".
//
// The nonce resolution is the caller's responsibility. Callers should
// derive it from an injected libtime.CurrentDateTimeGetter; the helper
// itself is a pure function over its inputs.
func DeriveTaskIDForce(owner, repo string, number int, sha, nonce string) uuid.UUID {
	key := fmt.Sprintf("%s/%s#%d@%s!%s", owner, repo, number, sha, nonce)
	return uuid.NewSHA1(prWatcherNamespace, []byte(key))
}
