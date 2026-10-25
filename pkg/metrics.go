// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strconv"
	"sync"

	libtime "github.com/bborbe/time"
	"github.com/prometheus/client_golang/prometheus"
)

//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics exposes counters for observable watcher behaviour.
type Metrics interface {
	// IncPollCycle increments the poll cycle counter with the given result label.
	// result: "success", "rate_limited", "github_error"
	IncPollCycle(result string)
	// IncPRPublished increments the PR-published counter with the given command label.
	// command: "create", "update_frontmatter", "skipped", "error", "override",
	// "override_skipped", "override_mechanical"
	IncPRPublished(command string)
	// IncWebhookDelivery increments the webhook delivery counter with the given result label.
	// result: "success", "skip"
	IncWebhookDelivery(result string)
	// IncWebhookSignatureRejected increments the webhook signature-rejection counter.
	IncWebhookSignatureRejected()
	// ObserveWebhookDispatchLatency records the dispatch latency of a webhook delivery.
	ObserveWebhookDispatchLatency(seconds float64)
	// SetRateLimitRemaining records the primary rate-limit window's remaining
	// requests (shared App token). The alert rule on this gauge fires BEFORE
	// quota exhaustion, catching the 2026-08-23 silent fleet-wide stall.
	SetRateLimitRemaining(remaining int)
	// SetUnreviewedPRs replaces the set of PRs reported on the
	// github_pr_watcher_unreviewed_prs gauge and records scannedAt on the
	// last-successful-scan timestamp gauge. The slice is the complete result
	// of one scan, so an empty slice clears the gauge.
	SetUnreviewedPRs(prs []UnreviewedPR, scannedAt libtime.DateTime)
	// IncUnreviewedRetry increments the retry counter with the given repo label.
	// repo: `owner/repo`
	IncUnreviewedRetry(repo string)
}

// UnreviewedPR identifies one open pull request reported as unreviewed.
type UnreviewedPR struct {
	// Repo is the `owner/repo` pair, matching the gauge's repo label.
	Repo string
	// Number is the pull-request number, rendered as the gauge's pr label.
	Number int
	// RetriesExhausted marks a PR whose retry budget is used up and whose last
	// retry has had a full threshold without a review, so the scan gave up on
	// it. Rendered as the gauge's retries_exhausted label.
	RetriesExhausted bool
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

	rateLimitRemaining = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "github_pr_watcher_rate_limit_remaining",
		Help: "Requests remaining in the GitHub primary rate-limit window (shared App token). Zero until the first API response populates it.",
	})

	unreviewedPRs = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "github_pr_watcher_unreviewed_prs",
		Help: "Open pull requests the watcher would hand to the reviewer whose current head SHA has no review from a configured reviewer bot after waiting longer than the unreviewed threshold. Value 1 per such PR; retries_exhausted is true once the bounded re-publish budget is used up and the last retry has had a full threshold without a review.",
	}, []string{"repo", "pr", "retries_exhausted"})

	unreviewedRetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "github_pr_watcher_unreviewed_retries_total",
		Help: "Total number of review tasks re-published by the unreviewed-PR retry pass, by repo. Bounded by UNREVIEWED_RETRY_MAX per (PR, head SHA) per process lifetime.",
	}, []string{"repo"})

	unreviewedScanLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "github_pr_watcher_unreviewed_scan_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last complete unreviewed-PR scan. Zero until the first successful scan; alerts on it must guard > 0.",
	})
)

func init() {
	prometheus.MustRegister(
		pollCyclesTotal,
		prPublishedTotal,
		webhookDeliveriesTotal,
		webhookSignatureRejectionsTotal,
		webhookDispatchLatencySeconds,
		rateLimitRemaining,
		unreviewedPRs,
		unreviewedScanLastSuccess,
		unreviewedRetriesTotal,
	)
	for _, result := range []string{"success", "rate_limited", "github_error"} {
		pollCyclesTotal.WithLabelValues(result).Add(0)
	}
	for _, cmd := range []string{"create", "update_frontmatter", "skipped", "error", "trust_error", "kafka_error", "override", "override_skipped", "override_mechanical", "deferred", "auto_merge", "auto_merge_skipped", "auto_merge_not_trivial", "auto_merge_labeled"} {
		prPublishedTotal.WithLabelValues(cmd).Add(0)
	}
	for _, result := range []string{"success", "skip"} {
		webhookDeliveriesTotal.WithLabelValues(result).Add(0)
	}
}

type prometheusMetrics struct {
	// mu serialises SetUnreviewedPRs: /check can run Poll concurrently with
	// the ticker, and the Reset()+Set() pair must not interleave.
	mu sync.Mutex
}

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

func (m *prometheusMetrics) SetRateLimitRemaining(remaining int) {
	rateLimitRemaining.Set(float64(remaining))
}

func (m *prometheusMetrics) SetUnreviewedPRs(prs []UnreviewedPR, scannedAt libtime.DateTime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Scans are serialised by the scanner's mutex, so a late older scan cannot
	// overwrite a newer result. m.mu only serialises concurrent SetUnreviewedPRs
	// callers — Prometheus scrapes do not take it, so a scrape can observe the
	// Reset+Set pair half-applied; the next scrape sees the full set.
	unreviewedPRs.Reset()
	for _, pr := range prs {
		unreviewedPRs.WithLabelValues(
			pr.Repo,
			strconv.Itoa(pr.Number),
			strconv.FormatBool(pr.RetriesExhausted),
		).Set(1)
	}
	unreviewedScanLastSuccess.Set(float64(scannedAt.Unix()))
}

func (m *prometheusMetrics) IncUnreviewedRetry(repo string) {
	unreviewedRetriesTotal.WithLabelValues(repo).Inc()
}
