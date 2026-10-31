// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/golang/glog"
)

// non2xxBodyLimit is the maximum number of runes of the response body carried
// in a non-2xx log line; longer bodies are truncated and suffixed with "…".
const non2xxBodyLimit = 200

// non2xxAbsent is logged for a header (or body) the response does not carry,
// so a missing value is distinguishable from an empty one.
const non2xxAbsent = "absent"

// logNon2xxV2 is the production sink for non-2xx lines. The watchers run with
// -v=2, so a V(2) line is visible in pod logs.
func logNon2xxV2(line string) {
	glog.V(2).Info(line)
}

// logNon2xxResponse emits one line for a response whose status is outside
// 200-299, carrying the signals needed to tell primary-quota exhaustion from
// secondary rate limiting. It reads the body to take a snippet and restores it,
// so the caller still sees the full body. Nothing is emitted for a 2xx.
func logNon2xxResponse(req *http.Request, resp *http.Response, log func(string)) {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return
	}
	var body []byte
	if resp.Body != nil {
		read, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(read))
		if err == nil {
			body = read
		}
	}
	log(non2xxLine(req, resp, body))
}

// non2xxLine formats the non-2xx line. Only method, host and path of the
// request are logged — never the full URL (userinfo, query tokens) and never a
// request header.
func non2xxLine(req *http.Request, resp *http.Response, body []byte) string {
	method, host, path := non2xxAbsent, non2xxAbsent, non2xxAbsent
	if req != nil {
		method = req.Method
		if req.URL != nil {
			host = orAbsent(req.URL.Host)
			path = orAbsent(req.URL.Path)
		}
	}
	return fmt.Sprintf(
		"github non-2xx method=%s host=%s path=%s status=%d resource=%s remaining=%s retry_after=%s body=%s",
		method,
		host,
		path,
		resp.StatusCode,
		orAbsent(resp.Header.Get("X-RateLimit-Resource")),
		orAbsent(resp.Header.Get("X-RateLimit-Remaining")),
		orAbsent(resp.Header.Get("Retry-After")),
		bodySnippet(body),
	)
}

func orAbsent(v string) string {
	if v == "" {
		return non2xxAbsent
	}
	return v
}

// bodySnippet collapses whitespace so the line stays one line, and truncates
// to non2xxBodyLimit runes with a trailing "…".
func bodySnippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return non2xxAbsent
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	if utf8.RuneCountInString(s) <= non2xxBodyLimit {
		return s
	}
	runes := []rune(s)
	return string(runes[:non2xxBodyLimit]) + "…"
}
