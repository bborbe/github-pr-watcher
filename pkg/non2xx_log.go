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

// non2xxPeekBytes bounds how much of a non-2xx body is buffered for the
// snippet: enough for non2xxBodyLimit runes of 4-byte UTF-8 plus whitespace.
const non2xxPeekBytes = 4096

// logNon2xxResponse emits one line for a response whose status is outside
// 200-299 — 3xx included, so a redirect hop or a 304 is logged too — carrying
// the signals needed to tell primary-quota exhaustion from secondary rate
// limiting. Nothing is emitted for a 2xx.
//
// Only the first non2xxPeekBytes of the body are read. The body handed back to
// the caller is that prefix followed by the unread remainder of the original
// body, so the caller sees the full body; a read error is not swallowed but
// re-surfaces on the caller's own read, after the prefix.
func logNon2xxResponse(req *http.Request, resp *http.Response, log func(string)) {
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return
	}
	var prefix []byte
	if resp.Body != nil {
		var readErr error
		prefix, readErr = io.ReadAll(io.LimitReader(resp.Body, non2xxPeekBytes))
		rest := io.Reader(resp.Body)
		if readErr != nil {
			rest = errReader{err: readErr}
		}
		resp.Body = peekedBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), rest),
			Closer: resp.Body,
		}
	}
	log(non2xxLine(req, resp, prefix))
}

// peekedBody replays a buffered prefix before the rest of the original body
// and closes the original body.
type peekedBody struct {
	io.Reader
	io.Closer
}

// errReader returns err on every read.
type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) {
	return 0, r.err
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
