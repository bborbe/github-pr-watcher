// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/bborbe/github-pr-watcher/pkg/command"
	libhttp "github.com/bborbe/http"
	libparse "github.com/bborbe/parse"
	"github.com/golang/glog"

	"github.com/bborbe/maintainer/lib/prurl"
)

//counterfeiter:generate -o ../../mocks/single_pr_trigger_handler.go --fake-name SinglePRTriggerHandler . SinglePRTriggerHandler

// SinglePRTriggerHandler handles POST /trigger?url=<pr_url>.
// The handler is intentionally thin: parse the URL, validate it
// synchronously, publish a TriggerPRReviewCommand to Kafka, and
// return HTTP 202. All GitHub API access, filter evaluation, and
// trust decision logic is owned by the in-pod command consumer.
type SinglePRTriggerHandler = libhttp.WithError

// NewSinglePRTriggerHandler returns a handler that publishes a
// TriggerPRReviewCommand to Kafka for each valid /trigger request.
func NewSinglePRTriggerHandler(
	sender command.TriggerPRReviewCommandSender,
) SinglePRTriggerHandler {
	return &singlePRTriggerHandler{
		sender: sender,
	}
}

type singlePRTriggerHandler struct {
	sender command.TriggerPRReviewCommandSender
}

func (h *singlePRTriggerHandler) ServeHTTP(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
) error {
	rawURL := req.URL.Query().Get("url")
	force := libparse.ParseBoolDefault(
		ctx,
		req.URL.Query().Get("force"),
		false,
	)
	if err := validateTriggerURL(ctx, rawURL); err != nil {
		return err
	}

	if err := h.sender.SendCommand(ctx, command.TriggerPRReviewCommand{
		URL:   rawURL,
		Force: force,
	}); err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "send TriggerPRReviewCommand"),
			http.StatusBadGateway,
		)
	}

	glog.V(2).Infof("trigger accepted url=%s", rawURL)
	return writeAccepted(resp, rawURL)
}

// validateTriggerURL rejects empty URLs, unparseable URLs, and
// non-GitHub platforms with HTTP 400. Mirrors the old parseAndValidateURL
// behavior so the 400 wire shape is unchanged for operators.
func validateTriggerURL(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return libhttp.WrapWithStatusCode(
			errors.Errorf(ctx, "url query parameter is required"),
			http.StatusBadRequest,
		)
	}
	prInfo, err := prurl.ParsePRURL(ctx, rawURL)
	if err != nil {
		return libhttp.WrapWithStatusCode(
			errors.Wrap(ctx, err, "parse PR URL"),
			http.StatusBadRequest,
		)
	}
	if prInfo.Platform != prurl.PlatformGitHub {
		return libhttp.WrapWithStatusCode(
			errors.Errorf(ctx, "only github platform is supported, got %s", prInfo.Platform),
			http.StatusBadRequest,
		)
	}
	return nil
}

// writeAccepted emits the 202 response with body {"status":"accepted","url":<raw>}.
func writeAccepted(resp http.ResponseWriter, rawURL string) error {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusAccepted)
	return json.NewEncoder(resp).Encode(map[string]interface{}{
		"status": "accepted",
		"url":    rawURL,
	})
}
