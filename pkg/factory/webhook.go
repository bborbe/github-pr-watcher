// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"context"

	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/handler"
	libtime "github.com/bborbe/time"
)

// CreateWebhookHandler wires the thin webhook receiver that publishes a
// TriggerPRReviewCommand to Kafka for each signature-verified pull_request
// delivery on /webhook/github-pr. Filter/trust work stays in the in-pod
// command consumer (shared with /trigger).
func CreateWebhookHandler(
	sender command.TriggerPRReviewCommandSender,
	coalescer coalesce.Coalescer,
	secret string,
	metrics handler.WebhookMetrics,
	clock libtime.CurrentDateTimeGetter,
) handler.WebhookHandler {
	return handler.NewWebhookHandler(sender, coalescer, secret, metrics, clock)
}

// CreateWebhookDispatcher returns the coalescer's trailing-edge dispatcher. It
// publishes the same TriggerPRReviewCommand the webhook receiver publishes on
// the leading edge, so a coalesced burst's trailing record travels the
// identical path — including the `Webhook` marker, which the command consumer
// reads to defer a mechanical-override author to the poll path.
func CreateWebhookDispatcher(
	sender command.TriggerPRReviewCommandSender,
	metrics handler.WebhookMetrics,
) coalesce.DispatchFunc {
	return (&webhookDispatcher{sender: sender, metrics: metrics}).Dispatch
}

// webhookDispatcher is the trailing-edge publisher. It is a type rather than an
// inline closure so this package stays free of conditionals in its factory
// bodies: the error branch and the metric calls live here, not in the factory.
type webhookDispatcher struct {
	sender  command.TriggerPRReviewCommandSender
	metrics handler.WebhookMetrics
}

// Dispatch publishes the trailing TriggerPRReviewCommand for one PR URL.
func (d *webhookDispatcher) Dispatch(ctx context.Context, url string) error {
	if err := d.sender.SendCommand(ctx, command.TriggerPRReviewCommand{
		URL:     url,
		Webhook: true,
	}); err != nil {
		d.metrics.IncWebhookDelivery("error")
		return err
	}
	d.metrics.IncWebhookDelivery("trailing")
	return nil
}
