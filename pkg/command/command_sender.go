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

	lib "github.com/bborbe/maintainer"
)

// sendCommandObject marshals payload into an event and publishes it as a
// command object on the GithubPRReviewV1SchemaID topic trio. It is the shared
// body of the three senders, so their wire shape and error wording cannot
// drift apart.
func sendCommandObject(
	ctx context.Context,
	commandCreator base.CommandCreator,
	initiator cqrsiam.Initiator,
	commandObjectSender cdb.CommandObjectSender,
	operation base.CommandOperation,
	payload any,
	typeName string,
) error {
	event, err := base.ParseEvent(ctx, payload)
	if err != nil {
		return errors.Wrapf(ctx, err, "parse %s event", typeName)
	}
	commandObject := cdb.CommandObject{
		Command:  commandCreator.NewCommand(operation, initiator, "", event),
		SchemaID: lib.GithubPRReviewV1SchemaID,
	}
	if err := commandObjectSender.SendCommandObject(ctx, commandObject); err != nil {
		return errors.Wrapf(ctx, err, "send %s to Kafka", typeName)
	}
	return nil
}
