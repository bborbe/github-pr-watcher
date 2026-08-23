// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rateCapturingTransport", func() {
	It("captures X-RateLimit-Remaining from each response", func() {
		var captured int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "7342")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		tr := &rateCapturingTransport{
			inner: srv.Client().Transport,
			set:   func(n int) { captured = n },
		}
		req, err := http.NewRequest("GET", srv.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		Expect(captured).To(Equal(7342))
	})
})

var _ = Describe("rateCapturingTransport edge cases", func() {
	It("leaves captured unchanged when the response has no rate-limit header", func() {
		var captured = -1
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		tr := &rateCapturingTransport{inner: srv.Client().Transport, set: func(n int) { captured = n }}
		req, err := http.NewRequest("GET", srv.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(captured).To(Equal(-1))
	})

	It("ignores a malformed non-integer header value", func() {
		var captured = -1
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "abc")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		tr := &rateCapturingTransport{inner: srv.Client().Transport, set: func(n int) { captured = n }}
		req, err := http.NewRequest("GET", srv.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(captured).To(Equal(-1))
	})
})
