// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
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
	secret string,
	metrics handler.WebhookMetrics,
	clock libtime.CurrentDateTimeGetter,
) handler.WebhookHandler {
	return handler.NewWebhookHandler(sender, secret, metrics, clock)
}
