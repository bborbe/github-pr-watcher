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

//counterfeiter:generate -o ../../mocks/set_task_link_status_command_sender.go --fake-name SetTaskLinkStatusCommandSender . SetTaskLinkStatusCommandSender

// SetTaskLinkStatusCommandSender sends SetTaskLinkStatusCommand payloads to
// Kafka. Calls Validate before publishing — a validation error is returned
// without touching Kafka.
type SetTaskLinkStatusCommandSender interface {
	SendCommand(ctx context.Context, cmd SetTaskLinkStatusCommand) error
}

// NewSetTaskLinkStatusCommandSender creates a SetTaskLinkStatusCommandSender.
// The commandCreator and initiator are injected at construction time per the
// cqrs/docs/producing-commands.md "Factory Wiring" pattern — built once at
// wiring, reused across every SendCommand call. The commandObjectSender wraps
// the Kafka sync producer.
func NewSetTaskLinkStatusCommandSender(
	commandCreator base.CommandCreator,
	initiator cqrsiam.Initiator,
	commandObjectSender cdb.CommandObjectSender,
) SetTaskLinkStatusCommandSender {
	return &setTaskLinkStatusCommandSender{
		commandCreator:      commandCreator,
		initiator:           initiator,
		commandObjectSender: commandObjectSender,
	}
}

type setTaskLinkStatusCommandSender struct {
	commandCreator      base.CommandCreator
	initiator           cqrsiam.Initiator
	commandObjectSender cdb.CommandObjectSender
}

func (s *setTaskLinkStatusCommandSender) SendCommand(
	ctx context.Context,
	cmd SetTaskLinkStatusCommand,
) error {
	if err := cmd.Validate(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate SetTaskLinkStatusCommand")
	}
	if err := sendCommandObject(
		ctx,
		s.commandCreator,
		s.initiator,
		s.commandObjectSender,
		SetTaskLinkStatusCommandOperation,
		cmd,
		"SetTaskLinkStatusCommand",
	); err != nil {
		return err
	}
	glog.V(2).Infof(
		"task-link status sender: published op=%s url=%s sha=%s bodyLen=%d",
		SetTaskLinkStatusCommandOperation,
		cmd.URL,
		cmd.HeadSHA,
		len(cmd.Body),
	)
	return nil
}
