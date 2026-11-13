// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package command

import (
	"context"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	"github.com/bborbe/errors"
	"github.com/golang/glog"

	lib "github.com/bborbe/maintainer"
)

//counterfeiter:generate -o ../../mocks/set_do_not_merge_review_command_sender.go --fake-name SetDoNotMergeReviewCommandSender . SetDoNotMergeReviewCommandSender

// SetDoNotMergeReviewCommandSender sends SetDoNotMergeReviewCommand payloads
// to Kafka. Calls Validate before publishing — a validation error is returned
// without touching Kafka.
type SetDoNotMergeReviewCommandSender interface {
	SendCommand(ctx context.Context, cmd SetDoNotMergeReviewCommand) error
}

// NewSetDoNotMergeReviewCommandSender creates a SetDoNotMergeReviewCommandSender.
// The commandCreator and initiator are injected at construction time per the
// cqrs/docs/producing-commands.md "Factory Wiring" pattern — built once at
// wiring, reused across every SendCommand call. The commandObjectSender wraps
// the Kafka sync producer.
func NewSetDoNotMergeReviewCommandSender(
	commandCreator base.CommandCreator,
	initiator cqrsiam.Initiator,
	commandObjectSender cdb.CommandObjectSender,
) SetDoNotMergeReviewCommandSender {
	return &setDoNotMergeReviewCommandSender{
		commandCreator:      commandCreator,
		initiator:           initiator,
		commandObjectSender: commandObjectSender,
	}
}

type setDoNotMergeReviewCommandSender struct {
	commandCreator      base.CommandCreator
	initiator           cqrsiam.Initiator
	commandObjectSender cdb.CommandObjectSender
}

func (s *setDoNotMergeReviewCommandSender) SendCommand(
	ctx context.Context,
	cmd SetDoNotMergeReviewCommand,
) error {
	if err := cmd.Validate(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate SetDoNotMergeReviewCommand")
	}
	event, err := base.ParseEvent(ctx, cmd)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse SetDoNotMergeReviewCommand event")
	}
	commandObject := cdb.CommandObject{
		Command: s.commandCreator.NewCommand(
			SetDoNotMergeReviewCommandOperation,
			s.initiator,
			"",
			event,
		),
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
	if err := s.commandObjectSender.SendCommandObject(ctx, commandObject); err != nil {
		return errors.Wrapf(ctx, err, "send SetDoNotMergeReviewCommand to Kafka")
	}
	glog.V(2).Infof(
		"do-not-merge review sender: published op=%s url=%s sha=%s",
		SetDoNotMergeReviewCommandOperation,
		cmd.URL,
		cmd.HeadSHA,
	)
	return nil
}
