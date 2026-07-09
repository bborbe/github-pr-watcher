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

//counterfeiter:generate -o ../../mocks/trigger_pr_review_command_sender.go --fake-name TriggerPRReviewCommandSender . TriggerPRReviewCommandSender

// TriggerPRReviewCommandSender sends TriggerPRReviewCommand payloads to
// Kafka. Calls Validate before publishing — a validation error is
// returned without touching Kafka.
type TriggerPRReviewCommandSender interface {
	SendCommand(ctx context.Context, cmd TriggerPRReviewCommand) error
}

// NewTriggerPRReviewCommandSender creates a TriggerPRReviewCommandSender.
// The commandCreator and initiator are injected at construction time per
// the cqrs/docs/producing-commands.md "Factory Wiring" pattern (matches
// trading/frontend/command's reference impl) — built once at wiring, reused
// across every SendCommand call. The commandObjectSender wraps the Kafka
// sync producer.
func NewTriggerPRReviewCommandSender(
	commandCreator base.CommandCreator,
	initiator cqrsiam.Initiator,
	commandObjectSender cdb.CommandObjectSender,
) TriggerPRReviewCommandSender {
	return &triggerPRReviewCommandSender{
		commandCreator:      commandCreator,
		initiator:           initiator,
		commandObjectSender: commandObjectSender,
	}
}

type triggerPRReviewCommandSender struct {
	commandCreator      base.CommandCreator
	initiator           cqrsiam.Initiator
	commandObjectSender cdb.CommandObjectSender
}

func (s *triggerPRReviewCommandSender) SendCommand(
	ctx context.Context,
	cmd TriggerPRReviewCommand,
) error {
	if err := cmd.Validate(ctx); err != nil {
		return errors.Wrapf(ctx, err, "validate TriggerPRReviewCommand")
	}
	event, err := base.ParseEvent(ctx, cmd)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse TriggerPRReviewCommand event")
	}
	commandObject := cdb.CommandObject{
		Command: s.commandCreator.NewCommand(
			TriggerPRReviewCommandOperation,
			s.initiator,
			"",
			event,
		),
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
	if err := s.commandObjectSender.SendCommandObject(ctx, commandObject); err != nil {
		return errors.Wrapf(ctx, err, "send TriggerPRReviewCommand to Kafka")
	}
	glog.V(2).
		Infof("trigger sender: published op=%s url=%s", TriggerPRReviewCommandOperation, cmd.URL)
	return nil
}
