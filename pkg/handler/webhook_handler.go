// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	libhttp "github.com/bborbe/http"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// WebhookHeader is a typed GitHub webhook header name, so header-name typos
// are caught by the type system.
type WebhookHeader string

const (
	WebhookSignatureHeader WebhookHeader = "X-Hub-Signature-256"
	WebhookEventHeader     WebhookHeader = "X-GitHub-Event"
)

// AvailableWebhookHeaders lists the webhook header names the handler reads.
var AvailableWebhookHeaders = []WebhookHeader{WebhookSignatureHeader, WebhookEventHeader}

//counterfeiter:generate -o ../../mocks/webhook_metrics.go --fake-name WebhookMetrics . WebhookMetrics

// WebhookMetrics is the narrow slice of watcher metrics the webhook handler
// records. pkg.Metrics satisfies it structurally, keeping this handler free
// of the pkg import — mirroring trigger_handler's thin dependency set.
type WebhookMetrics interface {
	IncWebhookDelivery(result string)
	IncWebhookSignatureRejected()
	ObserveWebhookDispatchLatency(seconds float64)
}

// WebhookHandler handles POST /webhook/github-pr.
// The handler is intentionally thin, mirroring SinglePRTriggerHandler: verify
// the GitHub HMAC signature, extract the PR URL from the pull_request event,
// publish a TriggerPRReviewCommand to Kafka, and return 202. All GitHub API
// access, filter evaluation, and trust logic stays in the in-pod command
// consumer (shared with /trigger), so gates 1–4 and the (repo, sha) task-ID
// dedup apply to webhook deliveries unchanged.
type WebhookHandler = libhttp.WithError

// NewWebhookHandler returns a handler that publishes a TriggerPRReviewCommand
// for each signature-verified pull_request webhook delivery.
func NewWebhookHandler(
	sender command.TriggerPRReviewCommandSender,
	secret string,
	metrics WebhookMetrics,
	clock libtime.CurrentDateTimeGetter,
) WebhookHandler {
	return &webhookHandler{
		sender:  sender,
		secret:  secret,
		metrics: metrics,
		clock:   clock,
	}
}

type webhookHandler struct {
	sender  command.TriggerPRReviewCommandSender
	secret  string
	metrics WebhookMetrics
	clock   libtime.CurrentDateTimeGetter
}

// dispatchActions are the pull_request actions worth triggering a review for.
// Everything else (labeled, closed, edited, ...) is acknowledged but dropped —
// the poll loop remains the backfill for anything this filter misses.
var dispatchActions = map[string]bool{
	"opened":           true,
	"reopened":         true,
	"synchronize":      true, // a push to the PR head
	"ready_for_review": true, // draft → ready
}

func (h *webhookHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) error {
	start := h.clock.Now()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "read webhook body"),
			http.StatusBadRequest,
		)
	}
	if err := verifyWebhookSignature(
		ctx,
		h.secret,
		req.Header.Get(string(WebhookSignatureHeader)),
		body,
	); err != nil {
		h.metrics.IncWebhookSignatureRejected()
		return libhttp.WrapWithStatusCode(err, http.StatusUnauthorized)
	}

	event := req.Header.Get(string(WebhookEventHeader))
	if event == "ping" {
		return writeWebhookAck(resp)
	}
	if event != "pull_request" {
		glog.V(2).Infof("webhook ignored event=%s", event)
		return writeWebhookAck(resp)
	}

	var payload webhookPullRequestEvent
	if err := json.Unmarshal(body, &payload); err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "parse webhook payload"),
			http.StatusBadRequest,
		)
	}
	if !dispatchActions[payload.Action] {
		glog.V(2).Infof("webhook ignored action=%s", payload.Action)
		h.metrics.IncWebhookDelivery("skip")
		return writeWebhookAck(resp)
	}
	if payload.PullRequest.HTMLURL == "" {
		return libhttp.WrapWithStatusCode(
			errors.Errorf(ctx, "webhook payload missing pull_request.html_url"),
			http.StatusBadRequest,
		)
	}

	if err := h.sender.SendCommand(ctx, command.TriggerPRReviewCommand{
		URL: payload.PullRequest.HTMLURL,
	}); err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "send TriggerPRReviewCommand"),
			http.StatusBadGateway,
		)
	}

	h.metrics.IncWebhookDelivery("success")
	h.metrics.ObserveWebhookDispatchLatency(h.clock.Now().Sub(start).Duration().Seconds())
	glog.V(2).Infof(
		"webhook accepted url=%s action=%s",
		payload.PullRequest.HTMLURL,
		payload.Action,
	)
	return writeWebhookDispatched(resp)
}

// webhookPullRequestEvent is the subset of a GitHub pull_request event the
// handler reads: the action plus the PR's html_url.
type webhookPullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
}

// verifyWebhookSignature checks the X-Hub-Signature-256 header ("sha256=<hex>")
// against an HMAC-SHA256 of the raw body, in constant time. An empty configured
// secret rejects everything (fail closed).
func verifyWebhookSignature(
	ctx context.Context,
	secret string,
	provided string,
	body []byte,
) error {
	if secret == "" {
		return errors.Errorf(ctx, "webhook secret not configured")
	}
	if provided == "" {
		return errors.Errorf(ctx, "missing webhook signature header")
	}
	_, sigHex, found := strings.Cut(provided, "=")
	if !found || sigHex == "" {
		return errors.Errorf(ctx, "malformed webhook signature header")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sigHex)) {
		return errors.Errorf(ctx, "invalid webhook signature")
	}
	return nil
}

// writeWebhookAck acknowledges a handled-but-not-dispatched delivery (ping,
// unsupported event, non-dispatch action) with 200 so GitHub does not retry.
func writeWebhookAck(resp http.ResponseWriter) error {
	resp.WriteHeader(http.StatusOK)
	return nil
}

// writeWebhookDispatched returns 202 with {"status":"accepted"} once the
// TriggerPRReviewCommand has been published to Kafka.
func writeWebhookDispatched(resp http.ResponseWriter) error {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusAccepted)
	return json.NewEncoder(resp).Encode(map[string]string{"status": "accepted"})
}
