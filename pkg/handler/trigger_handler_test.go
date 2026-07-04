// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/mocks"
	"github.com/bborbe/github-pr-watcher/pkg"
	"github.com/bborbe/github-pr-watcher/pkg/handler"
	libhttp "github.com/bborbe/http"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TriggerHandler", func() {
	var (
		ctx    context.Context
		sender *mocks.TriggerPRReviewCommandSender
		h      http.Handler
	)

	BeforeEach(func() {
		ctx = context.Background()
		sender = new(mocks.TriggerPRReviewCommandSender)
		h = libhttp.NewErrorHandler(handler.NewSinglePRTriggerHandler(sender))
	})

	DescribeTable(
		"error cases (400, no Kafka publish)",
		func(rawURL string) {
			sender.SendCommandReturns(nil) // should not be called
			req := httptest.NewRequest("POST", "/trigger?"+rawURL, nil)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusBadRequest))
			Expect(sender.SendCommandCallCount()).To(Equal(0),
				"SendCommand must not be called for invalid URL")
		},
		Entry("missing url returns 400", "foo=bar"),
		Entry("empty url returns 400", "url="),
		Entry("invalid url returns 400", "url=not-a-url"),
		Entry(
			"non-github platform returns 400",
			"url=https://bitbucket.org/owner/repo/pull-requests/1",
		),
	)

	Context("happy path: valid GitHub PR URL", func() {
		It("returns 202 with {status,url} body", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			var body map[string]interface{}
			Expect(json.Unmarshal(resp.Body.Bytes(), &body)).To(Succeed())
			Expect(body["status"]).To(Equal("accepted"))
			Expect(body["url"]).To(Equal("https://github.com/bborbe/repo/pull/42"))
		})

		It("publishes exactly one TriggerPRReviewCommand with the raw URL and Force=false", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.URL).To(Equal("https://github.com/bborbe/repo/pull/42"))
			Expect(sentCmd.Force).To(BeFalse())
		})
	})

	Context("Kafka send failure", func() {
		BeforeEach(func() {
			sender.SendCommandReturns(errors.Errorf(ctx, "kafka error"))
		})

		It("returns 502", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusBadGateway))
		})
	})

	Context("GitHub client off the request path (panicking GitHub client, spec 066 AC 5)", func() {
		// The handler must not depend on pkg.GitHubClient on the request
		// path. We assert this two ways:
		//   (a) structural — reflect.TypeOf the handler struct contains
		//       NO field whose type implements pkg.GitHubClient. This
		//       proves the dependency was actually removed (not just
		//       unused in tests).
		//   (b) behavioral — request completes with 202 even when no
		//       GitHubClient is wired anywhere in BeforeEach.
		It("handler struct has no GitHubClient-typed field", func() {
			// Build the handler directly (not via factory) so we can
			// reflect on the concrete struct.
			concrete := handler.NewSinglePRTriggerHandler(sender)
			// concrete is the SinglePRTriggerHandler alias of libhttp.WithError;
			// unwrap to the underlying value via the package's exported test seam.
			// The exported `singlePRTriggerHandler` struct is package-private;
			// we use reflect on the returned interface's dynamic type.
			t := reflect.TypeOf(concrete)
			// The returned value is the interface; get its dynamic type via Elem
			// (it's a pointer to a struct).
			if t.Kind() == reflect.Ptr {
				t = t.Elem()
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				ghType := reflect.TypeOf((*pkg.GitHubClient)(nil)).Elem()
				Expect(field.Type.Implements(ghType)).To(BeFalse(),
					"handler field %q (type %v) must not implement pkg.GitHubClient",
					field.Name, field.Type)
			}
		})
		It("request completes with 202 (no GitHubClient wired anywhere)", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusAccepted))
		})
	})

	Context("force query param (spec 067)", func() {
		It("TestTriggerHandler_ParsesForceTrue", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42&force=true",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.Force).To(BeTrue())
		})

		It("TestTriggerHandler_ParsesForceFalse", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42&force=false",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.Force).To(BeFalse())

			*sender = mocks.TriggerPRReviewCommandSender{}
			req2 := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp2 := httptest.NewRecorder()
			h.ServeHTTP(resp2, req2)
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd2 := sender.SendCommandArgsForCall(0)
			Expect(sentCmd2.Force).To(BeFalse())
		})

		It("TestTriggerHandler_ParsesForceAbsent", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.Force).To(BeFalse())
		})

		It("TestTriggerHandler_ParsesForceGarbage", func() {
			req := httptest.NewRequest(
				"POST",
				"/trigger?url=https://github.com/bborbe/repo/pull/42&force=banana",
				nil,
			)
			resp := httptest.NewRecorder()
			h.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusAccepted))
			Expect(sender.SendCommandCallCount()).To(Equal(1))
			_, sentCmd := sender.SendCommandArgsForCall(0)
			Expect(sentCmd.Force).To(BeFalse())
		})
	})
})
