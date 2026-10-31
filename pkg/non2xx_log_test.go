// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("non-2xx response logging", func() {
	var (
		lines []string
		sink  func(string)
	)
	BeforeEach(func() {
		lines = nil
		sink = func(l string) { lines = append(lines, l) }
	})

	roundTrip := func(status int, headers map[string]string, body string) *http.Response {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		DeferCleanup(srv.Close)
		tr := &rateCapturingTransport{
			inner: srv.Client().Transport,
			set:   func(int) {},
			log:   sink,
		}
		req, err := http.NewRequest("GET", srv.URL+"/repos/o/r/pulls", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	DescribeTable("emit predicate",
		func(status int, wantLine bool) {
			roundTrip(status, nil, "x")
			if wantLine {
				Expect(lines).To(HaveLen(1))
				Expect(lines[0]).To(HavePrefix("github non-2xx "))
			} else {
				Expect(lines).To(BeEmpty())
			}
		},
		Entry("200 emits nothing", http.StatusOK, false),
		Entry("204 emits nothing", http.StatusNoContent, false),
		Entry("304 emits a line", http.StatusNotModified, true),
		Entry("403 emits a line", http.StatusForbidden, true),
		Entry("404 emits a line", http.StatusNotFound, true),
		Entry("429 emits a line", http.StatusTooManyRequests, true),
		Entry("500 emits a line", http.StatusInternalServerError, true),
	)

	It("carries all fields when the headers are present", func() {
		roundTrip(http.StatusTooManyRequests, map[string]string{
			"X-RateLimit-Resource":  "core",
			"X-RateLimit-Remaining": "0",
			"Retry-After":           "60",
		}, `{"message":"API rate limit exceeded"}`)
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(ContainSubstring("method=GET "))
		Expect(lines[0]).To(ContainSubstring(" path=/repos/o/r/pulls "))
		Expect(lines[0]).To(ContainSubstring(" status=429 "))
		Expect(lines[0]).To(ContainSubstring(" resource=core "))
		Expect(lines[0]).To(ContainSubstring(" remaining=0 "))
		Expect(lines[0]).To(ContainSubstring(" retry_after=60 "))
		Expect(lines[0]).To(HaveSuffix(` body={"message":"API rate limit exceeded"}`))
	})

	DescribeTable("logs the literal absent for a missing header",
		func(missing string, field string) {
			headers := map[string]string{
				"X-RateLimit-Resource":  "core",
				"X-RateLimit-Remaining": "10",
				"Retry-After":           "5",
			}
			delete(headers, missing)
			roundTrip(http.StatusForbidden, headers, "denied")
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]).To(ContainSubstring(" " + field + "=absent "))
		},
		Entry("X-RateLimit-Resource", "X-RateLimit-Resource", "resource"),
		Entry("X-RateLimit-Remaining", "X-RateLimit-Remaining", "remaining"),
		Entry("Retry-After", "Retry-After", "retry_after"),
	)

	It("logs body=absent for an empty body", func() {
		roundTrip(http.StatusBadGateway, nil, "")
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=absent"))
	})

	DescribeTable("truncates the body snippet",
		func(n int, wantEllipsis bool) {
			roundTrip(http.StatusInternalServerError, nil, strings.Repeat("a", n))
			Expect(lines).To(HaveLen(1))
			snippet := lines[0][strings.Index(lines[0], " body=")+len(" body="):]
			if wantEllipsis {
				Expect(snippet).To(Equal(strings.Repeat("a", 200) + "…"))
			} else {
				Expect(snippet).To(Equal(strings.Repeat("a", n)))
			}
		},
		Entry("200 chars kept whole", 200, false),
		Entry("201 chars truncated", 201, true),
	)

	It("keeps the line on one line for a multi-line body", func() {
		roundTrip(http.StatusInternalServerError, nil, "line one\nline two\r\n\tline three")
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).NotTo(ContainSubstring("\n"))
		Expect(lines[0]).To(HaveSuffix(" body=line one line two line three"))
	})

	It("restores the response body for the caller", func() {
		resp := roundTrip(http.StatusNotFound, nil, `{"message":"Not Found"}`)
		read, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(read)).To(Equal(`{"message":"Not Found"}`))
	})

	It("never logs credentials from the request", func() {
		const sentinel = "ghs_SENTINELtoken123"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		DeferCleanup(srv.Close)
		tr := &rateCapturingTransport{inner: srv.Client().Transport, set: func(int) {}, log: sink}
		u := strings.Replace(srv.URL, "http://", "http://x-access-token:"+sentinel+"@", 1)
		req, err := http.NewRequest("GET", u+"/repos/o/r?access_token="+sentinel, nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+sentinel)
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).NotTo(ContainSubstring("SENTINEL"))
		Expect(lines[0]).NotTo(ContainSubstring("Bearer"))
		Expect(lines[0]).NotTo(ContainSubstring("access_token"))
	})

	It("restores a body larger than the peek window in full", func() {
		big := strings.Repeat("x", non2xxPeekBytes*3+7)
		resp := roundTrip(http.StatusInternalServerError, nil, big)
		read, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(read)).To(Equal(big))
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=" + strings.Repeat("x", 200) + "…"))
	})

	It("re-surfaces a body read error to the caller instead of swallowing it", func() {
		readErr := errors.New("connection reset mid-body")
		resp := &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{},
			Body: io.NopCloser(
				io.MultiReader(strings.NewReader("partial"), errReader{err: readErr}),
			),
		}
		logNon2xxResponse(newNon2xxRequest(), resp, sink)
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]).To(HaveSuffix(" body=partial"))
		read, err := io.ReadAll(resp.Body)
		Expect(string(read)).To(Equal("partial"))
		Expect(err).To(MatchError(readErr))
	})

	It("closes the original body when the caller closes the restored one", func() {
		closed := false
		resp := &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{},
			Body:       closeSpy{Reader: strings.NewReader("nf"), closed: &closed},
		}
		logNon2xxResponse(newNon2xxRequest(), resp, sink)
		Expect(resp.Body.Close()).To(Succeed())
		Expect(closed).To(BeTrue())
	})

	It("logs body=absent when the response has no body", func() {
		logNon2xxResponse(
			newNon2xxRequest(),
			&http.Response{StatusCode: http.StatusNotModified, Header: http.Header{}},
			sink,
		)
		Expect(lines).To(HaveLen(1))
		Expect(
			lines[0],
		).To(HaveSuffix(" status=304 resource=absent remaining=absent retry_after=absent body=absent"))
	})

	It("logs absent for a nil request and for a request without a URL", func() {
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}
		Expect(
			non2xxLine(nil, resp, nil),
		).To(HavePrefix("github non-2xx method=absent host=absent path=absent status=403 "))
		Expect(
			non2xxLine(&http.Request{Method: "GET"}, resp, nil),
		).To(HavePrefix("github non-2xx method=GET host=absent path=absent status=403 "))
	})

	It("replaces invalid UTF-8 in the body snippet", func() {
		resp := &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}}
		Expect(non2xxLine(nil, resp, []byte{'o', 'k', 0xff, 0xfe})).To(HaveSuffix(" body=ok?"))
	})

	It("emits nothing when log is nil", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		DeferCleanup(srv.Close)
		tr := &rateCapturingTransport{inner: srv.Client().Transport, set: func(int) {}}
		req, err := http.NewRequest("GET", srv.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tr.RoundTrip(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(BeEmpty())
	})
})

func newNon2xxRequest() *http.Request {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/o/r", nil)
	Expect(err).NotTo(HaveOccurred())
	return req
}

// closeSpy records whether Close was called.
type closeSpy struct {
	io.Reader
	closed *bool
}

func (c closeSpy) Close() error {
	*c.closed = true
	return nil
}
