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
	// IncWebhookDelivery increments the webhook delivery counter with the given result label.
	// result: "success", "skip"
	IncWebhookDelivery(result string)
	// IncWebhookSignatureRejected increments the webhook signature-rejection counter.
	IncWebhookSignatureRejected()
	// ObserveWebhookDispatchLatency records the dispatch latency of a webhook delivery.
	ObserveWebhookDispatchLatency(seconds float64)
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

	webhookDeliveriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "github_pr_watcher_webhook_deliveries_total",
		Help: "Total number of GitHub webhook deliveries by result.",
	}, []string{"result"})

	webhookSignatureRejectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "github_pr_watcher_webhook_signature_rejections_total",
		Help: "Total number of GitHub webhook payloads rejected for an invalid HMAC signature.",
	})

	webhookDispatchLatencySeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "github_pr_watcher_webhook_dispatch_latency_seconds",
		Help:    "Latency of dispatching a GitHub webhook delivery to Kafka.",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	prometheus.MustRegister(
		pollCyclesTotal,
		prPublishedTotal,
		webhookDeliveriesTotal,
		webhookSignatureRejectionsTotal,
		webhookDispatchLatencySeconds,
	)
	for _, result := range []string{"success", "rate_limited", "github_error"} {
		pollCyclesTotal.WithLabelValues(result).Add(0)
	}
	for _, cmd := range []string{"create", "update_frontmatter", "skipped", "error", "trust_error", "kafka_error", "override", "override_skipped", "auto_merge", "auto_merge_skipped"} {
		prPublishedTotal.WithLabelValues(cmd).Add(0)
	}
	for _, result := range []string{"success", "skip"} {
		webhookDeliveriesTotal.WithLabelValues(result).Add(0)
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

func (m *prometheusMetrics) IncWebhookDelivery(result string) {
	webhookDeliveriesTotal.WithLabelValues(result).Inc()
}

func (m *prometheusMetrics) IncWebhookSignatureRejected() {
	webhookSignatureRejectionsTotal.Inc()
}

func (m *prometheusMetrics) ObserveWebhookDispatchLatency(seconds float64) {
	webhookDispatchLatencySeconds.Observe(seconds)
}
