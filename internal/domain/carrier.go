package domain

import "time"

// RateLimitConfig specifies the token-bucket parameters for a carrier.
type RateLimitConfig struct {
	// TokensPerSecond is the sustained request rate (tokens refilled per second).
	TokensPerSecond float64
	// Burst is the maximum number of tokens that can accumulate (burst capacity).
	Burst int
}

// CarrierConfig holds tuning parameters for a single carrier integration.
type CarrierConfig struct {
	// TimeoutHint is the expected typical response latency used as the EMA seed.
	TimeoutHint time.Duration
	// OpenTimeout is how long the circuit breaker stays Open before probing.
	OpenTimeout time.Duration
	// FailureThreshold is the number of consecutive failures that opens the circuit.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes in HalfOpen before
	// the circuit closes.
	SuccessThreshold int
	// HedgeMultiplier is applied to p95 latency to derive the hedge-fire threshold
	// (hedge fires when elapsed > p95 * HedgeMultiplier).
	HedgeMultiplier float64
	// EMAAlpha is the smoothing factor for the exponentially weighted moving average
	// (0 < α < 1; smaller = slower adaptation, larger = faster).
	// Deprecated: prefer EMAWindowSize; when EMAWindowSize > 0 the tracker
	// computes alpha as 2/(N+1) and ignores this field.
	EMAAlpha float64
	// EMAWindowSize is the EMA window size N. When > 0 the tracker derives
	// alpha = 2/(N+1) instead of using EMAAlpha directly.
	EMAWindowSize int
	// EMAWarmupObservations is the number of observations required before hedging
	// is enabled. During warm-up, HedgeThreshold returns math.MaxFloat64.
	EMAWarmupObservations int
	// RateLimit configures the per-carrier token-bucket rate limiter.
	RateLimit RateLimitConfig
	// Priority is used as a tiebreak when selecting a hedge candidate
	// (lower value = preferred candidate).
	Priority int
}

// Carrier is the domain identity of a carrier integration.
type Carrier struct {
	// ID is the unique machine-readable identifier for this carrier (e.g., "alpha").
	ID string
	// Name is the human-readable display name.
	Name string
	// Capabilities lists the lines of business this carrier can price.
	Capabilities []CoverageLine
	// Config holds the operational tuning parameters for this carrier.
	Config CarrierConfig
}
