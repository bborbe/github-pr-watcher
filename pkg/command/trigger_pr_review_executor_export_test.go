// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/filter"
	"github.com/bborbe/github-pr-watcher/pkg/trust"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
)

// RunTriggerPRReview re-exports the private runTriggerPRReview for
// the external test package. The _test.go suffix keeps this file
// out of production builds.
var RunTriggerPRReview = runTriggerPRReview

// Compile-time guard: keep the public surface tightly aligned with
// the internal helper. If runTriggerPRReview's signature ever drifts,
// this file fails to build and the test breakage is local.
var _ = func(
	ctx context.Context,
	tx libkv.Tx,
	obj cdb.CommandObject,
	ghClient pkg.GitHubClient,
	createSender task.CreateCommandSender,
	taskCreationFilter filter.TaskCreationFilter,
	trustDecision trust.Trust,
	stage string,
	maxSlugLen int,
	maxTitleLen int,
	taskSuffix string,
	metrics pkg.Metrics,
	currentDateTime libtime.CurrentDateTimeGetter,
) (*base.EventID, base.Event, error) {
	return runTriggerPRReview(
		ctx, tx, obj,
		ghClient, createSender, taskCreationFilter, trustDecision,
		stage, maxSlugLen, maxTitleLen, taskSuffix, metrics,
		currentDateTime,
	)
}
