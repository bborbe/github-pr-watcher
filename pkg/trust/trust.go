// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package trust provides the boolean-combinator trust-decision framework
// for the maintainer-watcher-github-pr. Callers compose leaves with And, Or, and Not
// to express compound trust policies without modifying the watcher core.
package trust

import (
	"context"
	"fmt"
	"strings"

	"github.com/bborbe/errors"
)

// PR is the trust-evaluation input derived from a GitHub pull request.
// Only the fields needed for trust decisions are included.
type PR struct {
	AuthorLogin string
}

// Result captures a trust decision and a human-readable description.
// The description is the audit trail: why was this PR trusted or denied?
type Result interface {
	Success() bool
	Description() string
}

type result struct {
	success     bool
	description string
}

// NewResult constructs a Result with the given outcome and description.
func NewResult(success bool, description string) Result {
	return &result{success: success, description: description}
}

func (r *result) Success() bool { return r.success }

func (r *result) Description() string { return r.description }

//counterfeiter:generate -o ../../mocks/trust.go --fake-name Trust . Trust

// Trust is the single-method trust-decision interface.
// Each implementation returns both an outcome (trusted/denied) and a
// human-readable description forming the audit trail for the decision.
type Trust interface {
	IsTrusted(ctx context.Context, pr PR) (Result, error)
}

// Func adapts a plain function to the Trust interface.
// Useful for inline trust checks without struct boilerplate.
type Func func(ctx context.Context, pr PR) (Result, error)

func (f Func) IsTrusted(ctx context.Context, pr PR) (Result, error) {
	return f(ctx, pr)
}

// And succeeds only if every member succeeds.
// Evaluates ALL members (no short-circuit) to produce a complete audit trail.
// An empty And returns vacuous success — callers must ensure non-empty slices
// for security-relevant decisions (startup validation enforces this).
type And []Trust

func (a And) IsTrusted(ctx context.Context, pr PR) (Result, error) {
	var descriptions []string
	success := true
	for _, t := range a {
		r, err := t.IsTrusted(ctx, pr)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "and trust check")
		}
		if !r.Success() {
			success = false
		}
		descriptions = append(descriptions, fmt.Sprintf("[%t] %s", r.Success(), r.Description()))
	}
	return NewResult(success, "and("+strings.Join(descriptions, ", ")+")"), nil
}

// Or succeeds if any member succeeds.
// Evaluates ALL members (no short-circuit) to produce a complete audit trail.
// An empty Or returns vacuous failure — callers must ensure non-empty slices
// for security-relevant decisions.
type Or []Trust

func (o Or) IsTrusted(ctx context.Context, pr PR) (Result, error) {
	var descriptions []string
	success := false
	for _, t := range o {
		r, err := t.IsTrusted(ctx, pr)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "or trust check")
		}
		if r.Success() {
			success = true
		}
		descriptions = append(descriptions, fmt.Sprintf("[%t] %s", r.Success(), r.Description()))
	}
	return NewResult(success, "or("+strings.Join(descriptions, ", ")+")"), nil
}

// Not inverts the decision of the wrapped Trust, preserving the description.
func Not(t Trust) Trust {
	return Func(func(ctx context.Context, pr PR) (Result, error) {
		r, err := t.IsTrusted(ctx, pr)
		if err != nil {
			return nil, errors.Wrap(ctx, err, "not trust check")
		}
		return NewResult(!r.Success(), "not("+r.Description()+")"), nil
	})
}
