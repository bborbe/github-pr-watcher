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

//counterfeiter:generate -o ../../mocks/set_do_not_merge_status_command_sender.go --fake-name SetDoNotMergeStatusCommandSender . SetDoNotMergeStatusCommandSender

// SetDoNotMergeStatusCommandSender sends SetDoNotMergeStatusCommand payloads
// to Kafka. Calls Validate before publishing — a validation error is returned
// without touching Kafka.
type SetDoNotMergeStatusCommandSender interface {
	SendCommand(ctx context.Context, cmd SetDoNotMergeStatusCommand) error
}

// NewSetDoNotMergeStatusCommandSender creates a SetDoNotMergeStatusCommandSender.
// The commandCreator and initiator are injected at construction time per the
// cqrs/docs/producing-commands.md "Factory Wiring" pattern — built once at
// wiring, reused across every SendCommand call. The commandObjectSender wraps
// the Kafka sync producer.
func NewSetDoNotMergeStatusCommandSender(
	commandCreator base.CommandCreator,
	initiator cqrsiam.Initiator,
	commandObjectSender cdb.CommandObjectSender,
) SetDoNotMergeStatusCommandSender {
	return &setDoNotMergeStatusCommandSender{
		commandCreator:      commandCreator,
		initiator:           initiator,
		commandObjectSender: commandObjectSender,
	}
}

type setDoNotMergeStatusCommandSender struct {
	commandCreator      base.CommandCreator
	initiator           cqrsiam.Initiator
	commandObjectSender cdb.CommandObjectSender
}

func (s *setDoNotMergeStatusCommandSender) SendCommand(
	ctx context.Context,
	cmd SetDoNotMergeStatusCommand,
) error {
	if err := cmd.Validate(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate SetDoNotMergeStatusCommand")
	}
	event, err := base.ParseEvent(ctx, cmd)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse SetDoNotMergeStatusCommand event")
	}
	commandObject := cdb.CommandObject{
		Command: s.commandCreator.NewCommand(
			SetDoNotMergeStatusCommandOperation,
			s.initiator,
			"",
			event,
		),
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
	if err := s.commandObjectSender.SendCommandObject(ctx, commandObject); err != nil {
		return errors.Wrapf(ctx, err, "send SetDoNotMergeStatusCommand to Kafka")
	}
	glog.V(2).Infof(
		"do-not-merge status sender: published op=%s url=%s sha=%s",
		SetDoNotMergeStatusCommandOperation,
		cmd.URL,
		cmd.HeadSHA,
	)
	return nil
}
