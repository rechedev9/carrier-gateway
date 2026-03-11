package domain

import "errors"

// Sentinel errors for domain-specific failure modes.
// Callers MUST use errors.Is to check for these — never compare directly.
var (
	// ErrCircuitOpen is returned when a carrier's circuit breaker is in the Open
	// or HalfOpen-at-capacity state. The caller should not retry immediately.
	ErrCircuitOpen = errors.New("circuit open")

	// ErrRateLimitExceeded is returned when the per-carrier token bucket is exhausted
	// and the request context is cancelled before a token becomes available.
	ErrRateLimitExceeded = errors.New("rate limited")

	// ErrNoEligibleCarriers is returned by the orchestrator when no registered
	// carrier has capabilities matching the requested coverage lines, or all
	// eligible carriers have open circuit breakers.
	ErrNoEligibleCarriers = errors.New("no eligible carriers")

	// ErrCarrierTimeout is returned when the overall quote request timeout
	// expires before all carrier goroutines complete.
	ErrCarrierTimeout = errors.New("request timeout")

	// ErrCarrierUnavailable is returned by a carrier adapter when the carrier
	// returns a transient error (e.g., 5xx HTTP response, connection refused).
	ErrCarrierUnavailable = errors.New("carrier unavailable")

	// ErrInvalidRequest is returned when a request fails validation at the
	// API boundary (e.g., missing required fields, out-of-range values).
	ErrInvalidRequest = errors.New("invalid request")
)
