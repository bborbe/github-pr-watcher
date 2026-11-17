// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//nolint:dupl // Parallel sender types share shape; the publish logic is in sendCommandObject.
package command

import (
	"context"

	"github.com/bborbe/cqrs/base"
	cdb "github.com/bborbe/cqrs/cdb"
	cqrsiam "github.com/bborbe/cqrs/iam"
	"github.com/bborbe/errors"
	"github.com/golang/glog"
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
	if err := sendCommandObject(
		ctx,
		s.commandCreator,
		s.initiator,
		s.commandObjectSender,
		SetDoNotMergeStatusCommandOperation,
		cmd,
		"SetDoNotMergeStatusCommand",
	); err != nil {
		return err
	}
	glog.V(2).Infof(
		"do-not-merge status sender: published op=%s url=%s sha=%s",
		SetDoNotMergeStatusCommandOperation,
		cmd.URL,
		cmd.HeadSHA,
	)
	return nil
}
