// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"github.com/bborbe/github-pr-watcher/pkg/command"
	"github.com/bborbe/github-pr-watcher/pkg/handler"
)

// CreateSinglePRTriggerHandler wires the thin CQRS handler that publishes a
// TriggerPRReviewCommand to Kafka for each valid /trigger request.
// All GitHub/filter/trust work lives in the in-pod command consumer
// (see pkg/command.NewTriggerPRReviewCommandExecutor).
func CreateSinglePRTriggerHandler(
	sender command.TriggerPRReviewCommandSender,
) handler.SinglePRTriggerHandler {
	return handler.NewSinglePRTriggerHandler(sender)
}
