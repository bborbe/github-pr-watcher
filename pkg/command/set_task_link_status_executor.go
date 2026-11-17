// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg"
	libkv "github.com/bborbe/kv"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/prurl"
)

// NewSetTaskLinkStatusCommandExecutor creates a cdb.CommandObjectExecutorTx
// that consumes SetTaskLinkStatusCommand messages and posts the `task-link`
// commit status the master-protection rulesets require.
//
// It lives in the in-pod command consumer rather than the webhook handler so
// that GitHub API access stays in one place and a failed post is retried:
// the handler is a receiver, and an unretried network call there would turn a
// GitHub blip into a delivery GitHub never resends.
//
// Exit-path mapping:
//   - malformed payload / failed Validate  → cdb.ErrCommandObjectSkipped
//   - unparseable PR URL                   → cdb.ErrCommandObjectSkipped
//   - permanent GitHub error               → cdb.ErrCommandObjectSkipped (logged loudly)
//   - GitHub 5xx / network error           → wrapped error (transient, retried)
//   - success                              → nil, nil, nil
//
// SendResultEnabled is false: the post is fire-and-forget, matching the
// do-not-merge status executor.
func NewSetTaskLinkStatusCommandExecutor(
	ghClient pkg.GitHubClient,
) cdb.CommandObjectExecutorTx {
	return cdb.CommandObjectExecutorTxFunc(
		SetTaskLinkStatusCommandOperation,
		false, // SendResultEnabled = false
		func(
			ctx context.Context,
			_ libkv.Tx,
			commandObject cdb.CommandObject,
		) (*base.EventID, base.Event, error) {
			return runSetTaskLinkStatus(ctx, commandObject, ghClient)
		},
	)
}

func runSetTaskLinkStatus(
	ctx context.Context,
	commandObject cdb.CommandObject,
	ghClient pkg.GitHubClient,
) (*base.EventID, base.Event, error) {
	var cmd SetTaskLinkStatusCommand
	if err := commandObject.Command.Data.MarshalInto(ctx, &cmd); err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"malformed SetTaskLinkStatusCommand: %v",
			err,
		)
	}
	if err := cmd.Validate(ctx); err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"validate SetTaskLinkStatusCommand: %v",
			err,
		)
	}
	prInfo, err := prurl.ParsePRURL(ctx, cmd.URL)
	if err != nil {
		return nil, nil, errors.Wrapf(
			ctx,
			cdb.ErrCommandObjectSkipped,
			"parse url %q: %v",
			cmd.URL,
			err,
		)
	}

	state := pkg.CommitStateSuccess
	if !cmd.Linked() {
		state = pkg.CommitStateFailure
	}
	if err := ghClient.PostCommitStatus(
		ctx,
		prInfo.Owner,
		prInfo.Repo,
		cmd.HeadSHA,
		state,
		TaskLinkStatusContext,
	); err != nil {
		// A missing App permission or a gone repo will not succeed on a
		// redelivery, so it skips instead of retrying forever. Everything else
		// (5xx, network) stays a wrapped error and is retried.
		if errors.Is(err, pkg.ErrPermanent) {
			// Loud on purpose: a permanent post failure (most often the App
			// missing Commit statuses: Write) removes the required `task-link`
			// context, which blocks every merge in the repos that require it.
			// A silent skip would look healthy.
			glog.Errorf(
				"task-link status permanently failed, not retrying url=%s sha=%s: %v",
				cmd.URL,
				cmd.HeadSHA,
				err,
			)
			return nil, nil, errors.Wrapf(
				ctx,
				cdb.ErrCommandObjectSkipped,
				"permanent, not retrying: %v",
				err,
			)
		}
		return nil, nil, errors.Wrapf(
			ctx,
			err,
			"post %s status on %s@%s",
			TaskLinkStatusContext,
			cmd.URL,
			cmd.HeadSHA,
		)
	}
	glog.V(2).Infof(
		"task-link status posted url=%s sha=%s state=%s",
		cmd.URL,
		cmd.HeadSHA,
		state,
	)
	return nil, nil, nil
}
