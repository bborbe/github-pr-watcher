// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	stdtime "time"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg/coalesce"
	"github.com/bborbe/github-pr-watcher/pkg/handler"
	libhttp "github.com/bborbe/http"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const webhookTestSecret = "test-secret"

// signBody produces the X-Hub-Signature-256 value GitHub would send for a body.
func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// noOpCoalescer admits every push. A zero hold window makes every observation
// the leading edge, which is the pre-coalesce behaviour these cases assert.
func noOpCoalescer() coalesce.Coalescer {
	return coalesce.NewCoalescer(
		context.Background(),
		0,
		coalesce.NewTimerScheduler(),
		nil,
		libtime.NewCurrentDateTime(),
	)
}

var _ = Describe("WebhookHandler", func() {
	var (
		ctx     context.Context
		sender  *mocks.TriggerPRReviewCommandSender
		metrics *mocks.Metrics
		h       http.Handler
	)

	BeforeEach(func() {
		ctx = context.Background()
		sender = new(mocks.TriggerPRReviewCommandSender)
		metrics = new(mocks.Metrics)
		h = libhttp.NewErrorHandler(
			handler.NewWebhookHandler(
				sender,
				noOpCoalescer(),
				webhookTestSecret,
				metrics,
				libtime.NewCurrentDateTime(),
			),
		)
	})

	// webhookRequest builds a signed POST /webhook/github-pr with the given
	// GitHub event and raw payload body.
	webhookRequest := func(event, payload string) *http.Request {
		req := httptest.NewRequest("POST", "/webhook/github-pr", strings.NewReader(payload))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", signBody(webhookTestSecret, []byte(payload)))
		return req
	}

	pullRequestPayload := func(action string) string {
		return `{"action":"` + action + `","pull_request":{"html_url":"https://github.com/bborbe/repo/pull/42"}}`
	}

	Context("signature verification", func() {
		It(
			"rejects a missing signature with 401, no publish, increments rejection counter",
			func() {
				req := httptest.NewRequest(
					"POST",
					"/webhook/github-pr",
					strings.NewReader(pullRequestPayload("opened")),
				)
				req.Header.Set("X-GitHub-Event", "pull_request")
				resp := httptest.NewRecorder()
				h.ServeHTTP(resp, req)

				Expect(resp.Code).To(Equal(http.StatusUnauthorized))
				Expect(sender.SendCommandCallCount()).To(Equal(0))
				Expect(metrics.IncWebhookSignatureRejectedCallCount()).To(Equal(1))
			},
		)

		It(
			"rejects an invalid signature with 401, no publish, increments rejection counter",
			func() {
				payload := pullRequestPayload("opened")
				req := httptest.NewRequest("POST", "/webhook/github-pr", strings.NewReader(payload))
				req.Header.Set("X-GitHub-Event", "pull_request")
				req.Header.Set(
					"X-Hub-Signature-256",
					"sha256=0000000000000000000000000000000000000000000000000000000000000000",
				)
				resp := httptest.NewRecorder()
				h.ServeHTTP(resp, req)

				Expect(resp.Code).To(Equal(http.StatusUnauthorized))
				Expect(sender.SendCommandCallCount()).To(Equal(0))
				Expect(metrics.IncWebhookSignatureRejectedCallCount()).To(Equal(1))
			},
		)

		It("rejects everything when the secret is not configured (fail closed)", func() {
			closed := libhttp.NewErrorHandler(
				handler.NewWebhookHandler(
					sender,
					noOpCoalescer(),
					"",
					metrics,
					libtime.NewCurrentDateTime(),
				),
			)
			req := webhookRequest("pull_request", pullRequestPayload("opened"))
			resp := httptest.NewRecorder()
			closed.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusUnauthorized))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Context("event routing", func() {
		It("acks ping with 200 and no publish", func() {
			req := webhookRequest("ping", `{"zen":"keep it simple"}`)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
		})

		It("acks an unsupported event with 200 and no publish", func() {
			req := webhookRequest("push", `{}`)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
		})
	})

	Context("pull_request dispatch", func() {
		It("returns 202 and publishes the PR URL on opened", func() {
			req := webhookRequest("pull_request", pullRequestPayload("opened"))
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.URL).To(Equal("https://github.com/bborbe/repo/pull/42"))
			Expect(sentCmd.Force).To(BeFalse())
			Expect(metrics.IncWebhookDeliveryCallCount()).To(Equal(1))
			Expect(metrics.IncWebhookDeliveryArgsForCall(0)).To(Equal("success"))
			Expect(metrics.ObserveWebhookDispatchLatencyCallCount()).To(Equal(1))
		})

		It("returns 202 and publishes on synchronize", func() {
			req := webhookRequest("pull_request", pullRequestPayload("synchronize"))
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.URL).To(Equal("https://github.com/bborbe/repo/pull/42"))
		})

		It("returns 202 and publishes on ready_for_review", func() {
			req := webhookRequest("pull_request", pullRequestPayload("ready_for_review"))
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.URL).To(Equal("https://github.com/bborbe/repo/pull/42"))
		})

		It("acks a non-dispatch action with 200, no publish, counts skip", func() {
			req := webhookRequest("pull_request", pullRequestPayload("closed"))
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
			Expect(metrics.IncWebhookDeliveryCallCount()).To(Equal(1))
			Expect(metrics.IncWebhookDeliveryArgsForCall(0)).To(Equal("skip"))
		})

		It("rejects a malformed payload with 400", func() {
			req := webhookRequest("pull_request", `{not json`)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusBadRequest))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
		})

		It("rejects a payload without html_url with 400", func() {
			req := webhookRequest("pull_request", `{"action":"opened","pull_request":{}}`)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusBadRequest))
			Expect(sender.SendCommandCallCount()).To(Equal(0))
		})

		It("returns 502 when the Kafka send fails", func() {
			sender.SendCommandReturns(errors.Errorf(ctx, "kafka error"))
			req := webhookRequest("pull_request", pullRequestPayload("opened"))
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusBadGateway))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
		})
	})

	Context("trailing coalesce", func() {
		var (
			now       stdtime.Time
			fakeClock libtime.CurrentDateTimeGetter
			gate      coalesce.Coalescer
			hc        http.Handler
		)

		BeforeEach(func() {
			now = stdtime.Date(2026, 10, 27, 12, 0, 0, 0, stdtime.UTC)
			fakeClock = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			})
			gate = coalesce.NewCoalescer(
				context.Background(),
				3*stdtime.Minute,
				coalesce.NewTimerScheduler(),
				func(context.Context, string) error { return nil },
				fakeClock,
			)
			hc = libhttp.NewErrorHandler(
				handler.NewWebhookHandler(
					sender,
					gate,
					webhookTestSecret,
					metrics,
					fakeClock,
				),
			)
		})

		AfterEach(func() {
			gate.Close()
		})

		burstPayload := func(sha string) string {
			return `{"action":"synchronize","pull_request":{` +
				`"html_url":"https://github.com/bborbe/repo/pull/42",` +
				`"head":{"sha":"` + sha + `"}}}`
		}

		It("dispatches the first push of a burst and coalesces the next", func() {
			first := httptest.NewRecorder()
			hc.ServeHTTP(first, webhookRequest("pull_request", burstPayload("head-a")))

			Expect(first.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))

			now = now.Add(stdtime.Minute)
			second := httptest.NewRecorder()
			hc.ServeHTTP(second, webhookRequest("pull_request", burstPayload("head-b")))

			Expect(second.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			Expect(metrics.IncWebhookDeliveryArgsForCall(1)).To(Equal("coalesced"))
		})

		It("dispatches again once the PR has been quiet past the hold window", func() {
			first := httptest.NewRecorder()
			hc.ServeHTTP(first, webhookRequest("pull_request", burstPayload("head-a")))

			now = now.Add(10 * stdtime.Minute)
			second := httptest.NewRecorder()
			hc.ServeHTTP(second, webhookRequest("pull_request", burstPayload("head-b")))

			Expect(second.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(2))
		})

		It("dispatches ready_for_review even when the head SHA is unchanged", func() {
			// A draft PR's push is dispatched as the leading edge and dropped by
			// the consumer's draft gate. The ready_for_review transition carries
			// the same SHA, so coalescing it on the head would swallow it and the
			// PR would never be reviewed.
			draft := `{"action":"synchronize","pull_request":{` +
				`"html_url":"https://github.com/bborbe/repo/pull/42",` +
				`"head":{"sha":"head-a"}}}`
			first := httptest.NewRecorder()
			hc.ServeHTTP(first, webhookRequest("pull_request", draft))
			Expect(sender.SendCommandCallCount()).To(Equal(1))

			ready := `{"action":"ready_for_review","pull_request":{` +
				`"html_url":"https://github.com/bborbe/repo/pull/42",` +
				`"head":{"sha":"head-a"}}}`
			second := httptest.NewRecorder()
			hc.ServeHTTP(second, webhookRequest("pull_request", ready))

			Expect(second.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(2))
		})
	})
})
