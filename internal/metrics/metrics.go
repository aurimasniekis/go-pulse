// Package metrics holds pulsed's Prometheus registry (PL-OBS-002). Counters live here and are incremented by the
// feature packages; gauges are read at scrape time by Collector.
package metrics

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is pulsed's registry (not the global default, so tests and libraries cannot leak metrics into it).
var Registry = prometheus.NewRegistry()

var f = promauto.With(Registry)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

var (
	HTTPRequests = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_http_requests_total",
		Help: "HTTP requests by listener, route group, method and status code."}, []string{"listener", "route", "method", "code"})
	HTTPDuration = f.NewHistogramVec(prometheus.HistogramOpts{Name: "pulse_http_request_duration_seconds",
		Help: "HTTP request duration (SSE streams count until they end).", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}},
		[]string{"listener", "route"})
	IngestEvents = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_ingest_events_total",
		Help: "Events published by pulsed on behalf of a caller, by path (http, hook, admin)."}, []string{"via"})
	RollupEvents = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_rollup_events_total",
		Help: "Events counted by the rollup engine, by stream (all publishers, including raw NATS)."}, []string{"stream"})
	RollupBytes = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_rollup_bytes_total",
		Help: "Payload bytes counted by the rollup engine, by stream."}, []string{"stream"})
	AuthFailures = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_auth_failures_total",
		Help: "Authentication failures seen by the ban detector, by source."}, []string{"source"})
	Bans = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_bans_total",
		Help: "Bans created, by source (nats, http_token, webhook, manual)."}, []string{"source"})
	InboundHooks = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_inbound_hooks_total",
		Help: "Inbound webhook requests, by result (accepted, rejected, error)."}, []string{"result"})
	OutboundDeliveries = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_outbound_deliveries_total",
		Help: "Outbound webhook attempts, by outcome (delivered, retry, dead, manual, test)."}, []string{"outcome"})
	Notifications = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_notifications_total",
		Help: "Alert notifications, by notifier and result (sent, failed, suppressed)."}, []string{"notifier", "result"})
	AlertTransitions = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_alert_transitions_total",
		Help: "Alert state changes, by rule and state (firing, resolved)."}, []string{"rule", "state"})
	ACMEAttempts = f.NewCounterVec(prometheus.CounterOpts{Name: "pulse_acme_attempts_total",
		Help: "ACME issue/renew attempts, by result (ok, error)."}, []string{"result"})
)

// Handler serves the registry.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry, ErrorHandling: promhttp.ContinueOnError})
}

// Route maps a request path to a bounded route label (no IDs, names or free text).
func Route(path string) string {
	for _, p := range []string{"/api/admin/v1/", "/api/v1/"} {
		if rest, ok := strings.CutPrefix(path, p); ok {
			seg, _, _ := strings.Cut(rest, "/")
			if seg == "" || !routeSegment(seg) {
				return strings.TrimSuffix(p, "/") + "/other"
			}
			return p + seg
		}
	}
	switch {
	case strings.HasPrefix(path, "/hooks/"):
		return "/hooks"
	case path == "/ws":
		return "/ws"
	case strings.HasPrefix(path, "/api/"):
		return "/api/other"
	default:
		return "ui"
	}
}

// routeSegment accepts the API's static first path segments (lowercase words), which keeps the label set bounded.
func routeSegment(s string) bool {
	if len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
