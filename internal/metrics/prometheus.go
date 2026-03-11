// Package metrics provides a Prometheus-backed implementation of the
// ports.MetricsRecorder interface. All metrics are registered with an injected
// prometheus.Registerer so tests can use an isolated registry without polluting
// the global default.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// Metric name constants. Keeping these as named constants prevents typos and
// makes it easy to grep for metric names across the codebase.
const (
	metricCBState        = "carrier_circuit_breaker_state"
	metricP95Latency     = "carrier_p95_latency_ms"
	metricQuoteLatency   = "carrier_quote_latency_seconds"
	metricFanOutDuration = "orchestrator_fan_out_duration_seconds"
	metricRequestsTotal  = "carrier_requests_total"
	metricHedgesTotal    = "hedge_requests_total"
	metricCBTransitions  = "circuit_breaker_transitions_total"
	metricRateLimitTotal = "rate_limit_exceeded_total"
)

// quoteLatencyBuckets are histogram bucket boundaries (seconds) for
// carrier quote round-trip latency.
var quoteLatencyBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5}

// fanOutDurationBuckets are histogram bucket boundaries (seconds) for the
// total GetQuotes fan-out duration.
var fanOutDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// PrometheusRecorder implements ports.MetricsRecorder using Prometheus
// client_golang. All methods are safe for concurrent use.
type PrometheusRecorder struct {
	cbStateGauge       *prometheus.GaugeVec
	p95LatencyGauge    *prometheus.GaugeVec
	quoteLatencyHist   *prometheus.HistogramVec
	fanOutDurationHist prometheus.Histogram
	requestsTotal      *prometheus.CounterVec
	hedgesTotal        *prometheus.CounterVec
	cbTransitionsTotal *prometheus.CounterVec
	rateLimitTotal     *prometheus.CounterVec
}

// New registers all carrier-gateway metrics with reg and returns a
// PrometheusRecorder. Panics if any registration fails — this is a startup-time
// operation and misconfigured metrics should be caught immediately.
func New(reg prometheus.Registerer) *PrometheusRecorder {
	r := &PrometheusRecorder{}

	r.cbStateGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: metricCBState,
			Help: "Current circuit breaker state per carrier (0=closed, 1=open, 2=halfopen).",
		},
		[]string{"carrier_id"},
	)

	r.p95LatencyGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: metricP95Latency,
			Help: "Current EMA p95 latency in milliseconds per carrier.",
		},
		[]string{"carrier_id"},
	)

	r.quoteLatencyHist = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    metricQuoteLatency,
			Help:    "Carrier quote round-trip latency in seconds.",
			Buckets: quoteLatencyBuckets,
		},
		[]string{"carrier_id", "status"},
	)

	r.fanOutDurationHist = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    metricFanOutDuration,
			Help:    "Total duration of a GetQuotes fan-out in seconds.",
			Buckets: fanOutDurationBuckets,
		},
	)

	r.requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: metricRequestsTotal,
			Help: "Total number of carrier quote requests by outcome.",
		},
		[]string{"carrier_id", "status"},
	)

	r.hedgesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: metricHedgesTotal,
			Help: "Total number of hedge requests fired.",
		},
		[]string{"carrier_id", "trigger_carrier"},
	)

	r.cbTransitionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: metricCBTransitions,
			Help: "Total number of circuit breaker state transitions.",
		},
		[]string{"carrier_id", "from_state", "to_state"},
	)

	r.rateLimitTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: metricRateLimitTotal,
			Help: "Total number of requests dropped by the rate limiter.",
		},
		[]string{"carrier_id"},
	)

	reg.MustRegister(
		r.cbStateGauge,
		r.p95LatencyGauge,
		r.quoteLatencyHist,
		r.fanOutDurationHist,
		r.requestsTotal,
		r.hedgesTotal,
		r.cbTransitionsTotal,
		r.rateLimitTotal,
	)

	return r
}

// RecordQuote implements ports.MetricsRecorder.
func (r *PrometheusRecorder) RecordQuote(carrierID string, latency time.Duration, status string) {
	r.requestsTotal.WithLabelValues(carrierID, status).Inc()
	r.quoteLatencyHist.WithLabelValues(carrierID, status).Observe(latency.Seconds())
}

// RecordHedge implements ports.MetricsRecorder.
func (r *PrometheusRecorder) RecordHedge(hedgeCarrierID, triggerCarrierID string) {
	r.hedgesTotal.WithLabelValues(hedgeCarrierID, triggerCarrierID).Inc()
}

// RecordCBTransition implements ports.MetricsRecorder.
func (r *PrometheusRecorder) RecordCBTransition(carrierID string, from, to ports.CBState) {
	r.cbTransitionsTotal.WithLabelValues(carrierID, cbStateLabel(from), cbStateLabel(to)).Inc()
}

// RecordRateLimitRejection implements ports.MetricsRecorder.
func (r *PrometheusRecorder) RecordRateLimitRejection(carrierID string) {
	r.rateLimitTotal.WithLabelValues(carrierID).Inc()
}

// RecordFanOutDuration implements ports.MetricsRecorder.
func (r *PrometheusRecorder) RecordFanOutDuration(duration time.Duration) {
	r.fanOutDurationHist.Observe(duration.Seconds())
}

// SetCBState implements ports.MetricsRecorder.
func (r *PrometheusRecorder) SetCBState(carrierID string, state ports.CBState) {
	r.cbStateGauge.WithLabelValues(carrierID).Set(float64(state))
}

// SetP95Latency implements ports.MetricsRecorder.
func (r *PrometheusRecorder) SetP95Latency(carrierID string, ms float64) {
	r.p95LatencyGauge.WithLabelValues(carrierID).Set(ms)
}

// cbStateLabel converts a CBState to a human-readable label for Prometheus.
func cbStateLabel(s ports.CBState) string {
	switch s {
	case ports.CBStateClosed:
		return "closed"
	case ports.CBStateHalfOpen:
		return "halfopen"
	case ports.CBStateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// Compile-time assertion that PrometheusRecorder satisfies ports.MetricsRecorder.
var _ ports.MetricsRecorder = (*PrometheusRecorder)(nil)
