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
	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	libhttp "github.com/bborbe/http"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/prurl"
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
// for each signature-verified pull_request webhook delivery, unless the
// coalescer holds the push for its trailing dispatch.
func NewWebhookHandler(
	sender command.TriggerPRReviewCommandSender,
	statusSender command.SetDoNotMergeStatusCommandSender,
	coalescer coalesce.Coalescer,
	secret string,
	metrics WebhookMetrics,
	clock libtime.CurrentDateTimeGetter,
) WebhookHandler {
	return &webhookHandler{
		sender:       sender,
		statusSender: statusSender,
		coalescer:    coalescer,
		secret:       secret,
		metrics:      metrics,
		clock:        clock,
	}
}

type webhookHandler struct {
	sender       command.TriggerPRReviewCommandSender
	statusSender command.SetDoNotMergeStatusCommandSender
	coalescer    coalesce.Coalescer
	secret       string
	metrics      WebhookMetrics
	clock        libtime.CurrentDateTimeGetter
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

// statusActions are the pull_request actions that can change the
// `do-not-merge` commit status: the label itself, plus every action that
// presents a head SHA the ruleset will evaluate.
//
// This set is deliberately SEPARATE from dispatchActions. Widening
// dispatchActions instead would start a full review on every label event —
// labelled/unlabelled never change the head, so there is nothing to review,
// but the status must still be re-posted or the required context goes stale
// the moment the label moves.
//
// The status is published BEFORE shouldDispatch and never through the
// coalescer: coalescing collapses a burst of pushes into one trailing
// dispatch, which would leave a superseded head SHA without a status and
// block the merge on that SHA.
var statusActions = map[string]bool{
	"opened":           true,
	"reopened":         true,
	"synchronize":      true,
	"ready_for_review": true,
	"labeled":          true,
	"unlabeled":        true,
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
	// Validate before either send: a malformed payload is a 400 on every
	// action, not only on the ones that dispatch a review.
	if payload.PullRequest.HTMLURL == "" {
		return libhttp.WrapWithStatusCode(
			errors.Errorf(ctx, "webhook payload missing pull_request.html_url"),
			http.StatusBadRequest,
		)
	}
	// Post the do-not-merge commit status first, and outside the coalescer:
	// the required context must have a reporter on every head SHA the ruleset
	// evaluates, including a push the coalescer collapses.
	if err := h.postDoNotMergeStatus(ctx, payload); err != nil {
		return err
	}
	if !dispatchActions[payload.Action] {
		glog.V(2).Infof("webhook ignored action=%s", payload.Action)
		h.metrics.IncWebhookDelivery("skip")
		return writeWebhookAck(resp)
	}

	key, publish, err := h.shouldDispatch(ctx, payload)
	if err != nil {
		return libhttp.WrapWithStatusCode(err, http.StatusBadRequest)
	}
	if !publish {
		return writeWebhookDispatched(resp)
	}

	if err := h.sender.SendCommand(ctx, command.TriggerPRReviewCommand{
		URL:     payload.PullRequest.HTMLURL,
		Webhook: true,
	}); err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "send TriggerPRReviewCommand"),
			http.StatusBadGateway,
		)
	}
	// Only a publish that actually reached Kafka claims the head. A failed send
	// leaves it claimable, so GitHub's retry or the poll path can still emit it.
	h.coalescer.MarkEmitted(key, payload.PullRequest.Head.SHA, h.clock.Now())

	h.metrics.IncWebhookDelivery("success")
	h.metrics.ObserveWebhookDispatchLatency(h.clock.Now().Sub(start).Duration().Seconds())
	glog.V(2).Infof(
		"webhook accepted url=%s action=%s",
		payload.PullRequest.HTMLURL,
		payload.Action,
	)
	return writeWebhookDispatched(resp)
}

// postDoNotMergeStatus publishes the status command for a delivery that can
// change it, wrapping a send failure as a 502. A delivery outside statusActions
// is a no-op, so ServeHTTP needs no second branch.
func (h *webhookHandler) postDoNotMergeStatus(
	ctx context.Context,
	payload webhookPullRequestEvent,
) error {
	if !statusActions[payload.Action] {
		return nil
	}
	if err := h.sendDoNotMergeStatus(ctx, payload); err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "send SetDoNotMergeStatusCommand"),
			http.StatusBadGateway,
		)
	}
	return nil
}

// sendDoNotMergeStatus publishes the commit-status command for this delivery.
//
// The label set comes from the payload, not a second API read: a read issued
// here would race a rapid label/unlabel and could publish the state the PR
// had a moment ago. The executor derives success/failure from these labels.
func (h *webhookHandler) sendDoNotMergeStatus(
	ctx context.Context,
	payload webhookPullRequestEvent,
) error {
	if payload.PullRequest.HTMLURL == "" || payload.PullRequest.Head.SHA == "" {
		return errors.Errorf(
			ctx,
			"pull_request payload missing html_url or head.sha for action=%s",
			payload.Action,
		)
	}
	labels := make([]string, 0, len(payload.PullRequest.Labels))
	for _, label := range payload.PullRequest.Labels {
		labels = append(labels, label.Name)
	}
	if err := h.statusSender.SendCommand(ctx, command.SetDoNotMergeStatusCommand{
		URL:     payload.PullRequest.HTMLURL,
		HeadSHA: payload.PullRequest.Head.SHA,
		Labels:  labels,
	}); err != nil {
		return errors.Wrap(ctx, err, "send SetDoNotMergeStatusCommand")
	}
	glog.V(2).Infof(
		"webhook status posted url=%s sha=%s action=%s labels=%d",
		payload.PullRequest.HTMLURL,
		payload.PullRequest.Head.SHA,
		payload.Action,
		len(labels),
	)
	return nil
}

// coalescingActions are the deliveries that represent a new push to the head.
//
// Only these go through the coalescer. Every other dispatchable action is a
// state transition — opened, reopened, ready_for_review — and may carry an
// UNCHANGED head SHA. Gating those on the head would swallow them: a draft PR's
// push is dispatched as the leading edge and dropped by the consumer's draft
// gate, then `ready_for_review` arrives with the same SHA, the coalescer sees a
// head it already emitted and publishes nothing, and the poll settles that same
// head too — so the now-ready PR is never reviewed at all.
var coalescingActions = map[string]bool{
	"synchronize": true,
}

// shouldDispatch reports whether this delivery should publish now. Trailing
// coalesce: the first push of a burst is dispatched at once, every push behind
// it collapses into one trailing dispatch, so a burst of six pushes produces
// two review records rather than five. A false return with a nil error means
// the push was coalesced and nothing is published now.
func (h *webhookHandler) shouldDispatch(
	ctx context.Context,
	payload webhookPullRequestEvent,
) (coalesce.Key, bool, error) {
	prInfo, err := prurl.ParsePRURL(ctx, payload.PullRequest.HTMLURL)
	if err != nil {
		return coalesce.Key{}, false, errors.Wrap(ctx, err, "parse pull_request.html_url")
	}
	key := coalesce.Key{Owner: prInfo.Owner, Repo: prInfo.Repo, Number: prInfo.Number}
	if !coalescingActions[payload.Action] {
		return key, true, nil
	}
	if h.coalescer.Push(
		key,
		payload.PullRequest.Head.SHA,
		payload.PullRequest.HTMLURL,
		h.clock.Now(),
	) {
		return key, true, nil
	}
	h.metrics.IncWebhookDelivery("coalesced")
	glog.V(2).Infof(
		"webhook coalesced url=%s action=%s",
		payload.PullRequest.HTMLURL,
		payload.Action,
	)
	return key, false, nil
}

// webhookPullRequestEvent is the subset of a GitHub pull_request event the
// handler reads: the action, the PR's html_url, its head SHA, and its labels.
// Labels are read only to derive the do-not-merge commit status.
type webhookPullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
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
