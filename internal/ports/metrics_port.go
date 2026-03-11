package ports

import "time"

// CBState mirrors the circuit breaker state for metrics labelling and
// Prometheus gauge emission. Values are stable and must not change —
// they map directly to the numeric gauge value in Prometheus.
type CBState int32

const (
	// CBStateClosed represents normal operation (failures < threshold).
	CBStateClosed CBState = 0
	// CBStateOpen represents the tripped state (requests short-circuited).
	CBStateOpen CBState = 1
	// CBStateHalfOpen represents the probe state (circuit recovering).
	CBStateHalfOpen CBState = 2
)

// MetricsRecorder is the side-effect port for emitting operational metrics.
// All implementations must be safe for concurrent use.
// Callers must not hold locks when invoking these methods, as implementations
// may perform I/O (e.g., Prometheus registry operations).
type MetricsRecorder interface {
	// RecordQuote records the outcome of a single carrier quote attempt.
	// status must be one of: "success", "error", "circuit_open",
	// "rate_limited", "timeout".
	RecordQuote(carrierID string, latency time.Duration, status string)

	// RecordHedge records that a hedge request was fired.
	// hedgeCarrierID is the carrier the hedge was sent to.
	// triggerCarrierID is the slow carrier that triggered the hedge.
	RecordHedge(hedgeCarrierID, triggerCarrierID string)

	// RecordCBTransition records a circuit breaker state transition.
	RecordCBTransition(carrierID string, from, to CBState)

	// RecordRateLimitRejection records that a carrier request was dropped
	// because the rate limiter context expired before a token was available.
	RecordRateLimitRejection(carrierID string)

	// RecordFanOutDuration records the total duration of a GetQuotes fan-out.
	RecordFanOutDuration(duration time.Duration)

	// SetCBState sets the current circuit breaker state gauge for a carrier.
	SetCBState(carrierID string, state CBState)

	// SetP95Latency sets the current EMA p95 latency gauge for a carrier.
	// ms is the latency value in milliseconds.
	SetP95Latency(carrierID string, ms float64)
}
