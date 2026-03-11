// Package circuitbreaker provides a per-carrier 3-state circuit breaker
// implemented with sync/atomic operations. The three states are:
//
//	Closed   (0) — normal operation; requests flow through.
//	Open     (1) — tripped; requests are short-circuited immediately.
//	HalfOpen (2) — probe state; exactly one request is allowed through.
//
// All methods are safe for concurrent use.
package circuitbreaker

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// State constants mirror ports.CBState values and map directly to the
// atomic.Int32 stored in Breaker. They must not be renumbered.
const (
	stateClosed   int32 = 0
	stateOpen     int32 = 1
	stateHalfOpen int32 = 2
)

// defaultFailureThreshold is used when Config.FailureThreshold is zero.
const defaultFailureThreshold = 5

// defaultOpenTimeout is used when Config.OpenTimeout is zero.
const defaultOpenTimeout = 30 * time.Second

// Config holds the tuneable parameters for a Breaker instance.
type Config struct {
	// FailureThreshold is the number of consecutive failures that trips the
	// circuit from Closed to Open. Defaults to 5.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes in HalfOpen
	// before the circuit closes. Defaults to 1 when zero.
	SuccessThreshold int
	// OpenTimeout is how long the circuit stays Open before transitioning to
	// HalfOpen for a probe. Defaults to 30 seconds.
	OpenTimeout time.Duration
}

// Breaker is a per-carrier 3-state circuit breaker.
// The zero value is not valid — use New.
type Breaker struct {
	carrierID string
	cfg       Config
	metrics   ports.MetricsRecorder

	// state is the current CBState encoded as int32 for atomic CAS.
	state atomic.Int32
	// failures counts consecutive failures in the Closed state.
	failures atomic.Int32
	// successes counts consecutive successes in the HalfOpen state.
	successes atomic.Int32
	// halfOpenInFlight is 0 when no probe is in flight, 1 when one is.
	halfOpenInFlight atomic.Int32
	// openedAt is the Unix nanosecond timestamp when the circuit was last opened.
	openedAt atomic.Int64
}

// New returns a Breaker initialised to Closed state with defaults applied to
// zero-valued Config fields.
func New(carrierID string, cfg Config, m ports.MetricsRecorder) *Breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = defaultFailureThreshold
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 1
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = defaultOpenTimeout
	}
	b := &Breaker{
		carrierID: carrierID,
		cfg:       cfg,
		metrics:   m,
	}
	// Emit initial gauge so Prometheus is seeded from the start.
	m.SetCBState(carrierID, ports.CBStateClosed)
	return b
}

// State returns the current CBState of the breaker.
func (b *Breaker) State() ports.CBState {
	return ports.CBState(b.state.Load())
}

// Execute runs fn through the circuit breaker using the following rules:
//
//   - Closed: fn is called. On failure the failure counter increments; when it
//     reaches FailureThreshold the circuit trips to Open.
//   - Open: if OpenTimeout has not elapsed, domain.ErrCircuitOpen is returned
//     immediately without calling fn. After OpenTimeout the circuit transitions
//     to HalfOpen and falls through.
//   - HalfOpen: exactly one probe (halfOpenInFlight CAS 0→1) is allowed. If a
//     second concurrent call arrives it receives domain.ErrCircuitOpen. On probe
//     success the circuit closes after SuccessThreshold successes; on probe
//     failure it re-opens.
func (b *Breaker) Execute(ctx context.Context, fn func() error) error {
	current := b.state.Load()

	switch current {
	case stateOpen:
		if time.Since(time.Unix(0, b.openedAt.Load())) < b.cfg.OpenTimeout {
			return domain.ErrCircuitOpen
		}
		// OpenTimeout elapsed — attempt transition to HalfOpen.
		if b.state.CompareAndSwap(stateOpen, stateHalfOpen) {
			b.metrics.RecordCBTransition(b.carrierID, ports.CBStateOpen, ports.CBStateHalfOpen)
			b.metrics.SetCBState(b.carrierID, ports.CBStateHalfOpen)
		}
		// Whether we won the CAS or another goroutine beat us, fall through to
		// HalfOpen handling (current will be re-read below).
		return b.executeHalfOpen(ctx, fn)

	case stateHalfOpen:
		return b.executeHalfOpen(ctx, fn)

	default: // stateClosed
		return b.executeClosed(ctx, fn)
	}
}

// executeClosed runs fn in the Closed state, managing the failure counter and
// tripping to Open when FailureThreshold is reached.
func (b *Breaker) executeClosed(ctx context.Context, fn func() error) error {
	if err := fn(); err != nil {
		newFailures := b.failures.Add(1)
		if newFailures >= int32(b.cfg.FailureThreshold) {
			if b.state.CompareAndSwap(stateClosed, stateOpen) {
				b.openedAt.Store(time.Now().UnixNano())
				b.metrics.RecordCBTransition(b.carrierID, ports.CBStateClosed, ports.CBStateOpen)
				b.metrics.SetCBState(b.carrierID, ports.CBStateOpen)
			}
		}
		return err
	}
	// Success resets the failure counter.
	b.failures.Store(0)
	return nil
}

// executeHalfOpen enforces exactly-one probe concurrency and handles the
// HalfOpen→Closed and HalfOpen→Open transitions.
func (b *Breaker) executeHalfOpen(ctx context.Context, fn func() error) error {
	// Enforce HalfOpenMaxConc=1 via CAS.
	if !b.halfOpenInFlight.CompareAndSwap(0, 1) {
		return domain.ErrCircuitOpen
	}
	defer b.halfOpenInFlight.Store(0)

	if err := fn(); err != nil {
		// Probe failed — re-open immediately.
		if b.state.CompareAndSwap(stateHalfOpen, stateOpen) {
			b.openedAt.Store(time.Now().UnixNano())
			b.successes.Store(0)
			b.metrics.RecordCBTransition(b.carrierID, ports.CBStateHalfOpen, ports.CBStateOpen)
			b.metrics.SetCBState(b.carrierID, ports.CBStateOpen)
		}
		return err
	}

	// Probe succeeded.
	newSuccesses := b.successes.Add(1)
	if newSuccesses >= int32(b.cfg.SuccessThreshold) {
		if b.state.CompareAndSwap(stateHalfOpen, stateClosed) {
			b.failures.Store(0)
			b.successes.Store(0)
			b.metrics.RecordCBTransition(b.carrierID, ports.CBStateHalfOpen, ports.CBStateClosed)
			b.metrics.SetCBState(b.carrierID, ports.CBStateClosed)
		}
	}
	return nil
}

// Compile-time assertion that Breaker satisfies the Execute/State interface
// expected by the orchestrator.
var _ interface {
	Execute(context.Context, func() error) error
	State() ports.CBState
} = (*Breaker)(nil)
