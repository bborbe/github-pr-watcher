// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "github.com/prometheus/client_golang/prometheus"

//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics exposes counters for observable watcher behaviour.
type Metrics interface {
	// IncPollCycle increments the poll cycle counter with the given result label.
	// result: "success", "rate_limited", "github_error"
	IncPollCycle(result string)
	// IncPRPublished increments the PR-published counter with the given command label.
	// command: "create", "update_frontmatter", "skipped", "error", "override", "override_skipped"
	IncPRPublished(command string)
}

var (
	pollCyclesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "github_pr_watcher_poll_cycles_total",
		Help: "Total number of GitHub poll cycles by result.",
	}, []string{"result"})

	prPublishedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "github_pr_watcher_prs_total",
		Help: "Total number of PRs processed by command type.",
	}, []string{"command"})
)

func init() {
	prometheus.MustRegister(pollCyclesTotal, prPublishedTotal)
	for _, result := range []string{"success", "rate_limited", "github_error"} {
		pollCyclesTotal.WithLabelValues(result).Add(0)
	}
	for _, cmd := range []string{"create", "update_frontmatter", "skipped", "error", "trust_error", "kafka_error", "override", "override_skipped"} {
		prPublishedTotal.WithLabelValues(cmd).Add(0)
	}
}

type prometheusMetrics struct{}

// NewMetrics returns a Metrics implementation backed by Prometheus counters.
func NewMetrics() Metrics {
	return &prometheusMetrics{}
}

func (m *prometheusMetrics) IncPollCycle(result string) {
	pollCyclesTotal.WithLabelValues(result).Inc()
}

func (m *prometheusMetrics) IncPRPublished(command string) {
	prPublishedTotal.WithLabelValues(command).Inc()
}
