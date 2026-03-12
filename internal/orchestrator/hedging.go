// Package orchestrator contains the fan-out/fan-in engine, adaptive hedging
// tracker, and orchestrator struct. Files in this package are allowed to import
// circuitbreaker, ratelimiter, and adapter — but hedging.go avoids importing
// internal/adapter directly to prevent a dependency cycle.
package orchestrator

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// adapterExecFn is a type-erased carrier call, equivalent to
// adapter.AdapterFunc but defined locally to avoid importing internal/adapter
// in hedging.go (which would create a dependency graph concern).
type adapterExecFn func(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)

// emaState holds the current EMA p95 value and the observation count.
// It is replaced atomically on every update; never mutated in place.
type emaState struct {
	p95          float64
	observations int
}

// EMATracker maintains a per-carrier exponentially-weighted moving p95 latency.
// Safe for concurrent use via atomic pointer swap on the internal state struct.
type EMATracker struct {
	carrierID  string
	alpha      float64
	multiplier float64 // CarrierConfig.HedgeMultiplier
	warmup     int     // CarrierConfig.EMAWarmupObservations
	state      atomic.Pointer[emaState]
	metrics    ports.MetricsRecorder
}

// NewEMATracker returns an EMATracker seeded at 2×seed with warm-up enabled.
// seed should be the carrier's TimeoutHint.
func NewEMATracker(
	carrierID string,
	seed time.Duration,
	cfg domain.CarrierConfig,
	m ports.MetricsRecorder,
) *EMATracker {
	seedMs := float64(seed.Milliseconds()) * 2.0
	alpha := cfg.EMAAlpha
	if cfg.EMAWindowSize > 0 {
		alpha = 2.0 / (float64(cfg.EMAWindowSize) + 1)
	}
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.1
	}
	t := &EMATracker{
		carrierID:  carrierID,
		alpha:      alpha,
		multiplier: cfg.HedgeMultiplier,
		warmup:     cfg.EMAWarmupObservations,
		metrics:    m,
	}
	initial := &emaState{p95: seedMs, observations: 0}
	t.state.Store(initial)
	return t
}

// Record updates the EMA with a new latency observation and emits the p95 gauge.
// Uses an atomic pointer swap to remain lock-free.
func (t *EMATracker) Record(latency time.Duration) {
	latencyMs := float64(latency) / float64(time.Millisecond)
	for {
		old := t.state.Load()
		newP95 := t.alpha*latencyMs + (1-t.alpha)*old.p95
		next := &emaState{
			p95:          newP95,
			observations: old.observations + 1,
		}
		if t.state.CompareAndSwap(old, next) {
			t.metrics.SetP95Latency(t.carrierID, newP95)
			return
		}
		// Another goroutine updated first — retry with the fresh state.
	}
}

// P95 returns the current EMA p95 latency estimate in milliseconds.
func (t *EMATracker) P95() float64 {
	return t.state.Load().p95
}

// HedgeThreshold returns the latency in milliseconds at which a hedge should
// fire for this carrier.
//
// Returns math.MaxFloat64 during warm-up (observations < EMAWarmupObservations)
// to suppress premature hedging. After warm-up returns P95 × HedgeMultiplier.
func (t *EMATracker) HedgeThreshold() float64 {
	s := t.state.Load()
	if s.observations < t.warmup {
		return math.MaxFloat64
	}
	return s.p95 * t.multiplier
}

// pendingCarrier tracks the state of a single in-flight primary carrier call
// for the hedge monitor.
type pendingCarrier struct {
	startTime      time.Time
	hedgeThreshold float64 // milliseconds at which to fire a hedge
}

// hedgeCarrier describes a circuit-breaker/limiter-aware carrier available for
// hedging. Passed as a slice to hedgeMonitor to avoid importing circuitbreaker
// or ratelimiter package types from hedging.go.
type hedgeCarrier struct {
	carrier    domain.Carrier
	p95Ms      float64 // current EMA p95 for priority ordering
	tryAcquire func() bool
	cbState    func() ports.CBState
	exec       adapterExecFn
}

// hedgeMonitor runs as a goroutine alongside the primary fan-out goroutines.
// It polls every hedgePollInterval, and when a pending carrier has been
// waiting longer than its HedgeThreshold, fires a hedge goroutine to a
// different carrier.
//
// hedgeMonitor exits when ctx is Done or when all pending slots are resolved.
//
// Parameters are passed by value or as slices/maps to avoid data races; the
// results channel is the only shared mutable resource, and sends are
// non-blocking with a select guard.
func hedgeMonitor(
	ctx context.Context,
	pending map[string]pendingCarrier,
	results chan<- domain.QuoteResult,
	eligible []hedgeCarrier,
	req domain.QuoteRequest,
	metrics ports.MetricsRecorder,
	log *slog.Logger,
	pollInterval time.Duration,
) {
	var hedgeWg sync.WaitGroup
	defer hedgeWg.Wait()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// alreadyHedged tracks which primary carrier slots have had a hedge fired.
	alreadyHedged := make(map[string]bool, len(pending))

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now()
		allResolved := true

		for carrierID, p := range pending {
			if alreadyHedged[carrierID] {
				continue
			}
			allResolved = false

			elapsedMs := float64(now.Sub(p.startTime)) / float64(time.Millisecond)
			if elapsedMs < p.hedgeThreshold {
				continue
			}

			// Select the best hedge candidate: lowest p95, Priority as tiebreak,
			// excluding the slow carrier itself and any Open-CB carriers.
			candidate := selectHedgeCandidate(carrierID, eligible)
			if candidate == nil {
				log.Warn("no eligible hedge candidate",
					slog.String("request_id", req.RequestID),
					slog.String("trigger_carrier_id", carrierID),
				)
				continue
			}

			if !candidate.tryAcquire() {
				// Rate limiter empty — suppress hedge silently.
				continue
			}

			// Fire hedge goroutine.
			alreadyHedged[carrierID] = true
			metrics.RecordHedge(candidate.carrier.ID, carrierID)
			log.Info("hedge fired",
				slog.String("request_id", req.RequestID),
				slog.String("trigger_carrier_id", carrierID),
				slog.String("hedge_carrier_id", candidate.carrier.ID),
				slog.Float64("elapsed_ms", elapsedMs),
				slog.Float64("threshold_ms", p.hedgeThreshold),
			)

			execFn := candidate.exec
			hedgeCarrierID := candidate.carrier.ID
			hedgeWg.Add(1)
			go func() {
				defer hedgeWg.Done()
				result, err := execFn(ctx, req)
				if err != nil {
					return
				}
				result.IsHedged = true
				select {
				case results <- result:
				case <-ctx.Done():
				}
				log.Info("hedge result received",
					slog.String("request_id", req.RequestID),
					slog.String("carrier_id", hedgeCarrierID),
				)
			}()
		}

		if allResolved {
			return
		}
	}
}

// selectHedgeCandidate finds the best available hedge carrier — lowest p95
// with Priority as a tiebreak — excluding the triggering carrier and any
// carrier whose circuit breaker is Open.
func selectHedgeCandidate(excludeCarrierID string, eligible []hedgeCarrier) *hedgeCarrier {
	var best *hedgeCarrier
	for i := range eligible {
		c := &eligible[i]
		if c.carrier.ID == excludeCarrierID {
			continue
		}
		if c.cbState() == ports.CBStateOpen {
			continue
		}
		if best == nil {
			best = c
			continue
		}
		if c.p95Ms < best.p95Ms {
			best = c
		} else if c.p95Ms == best.p95Ms && c.carrier.Config.Priority < best.carrier.Config.Priority {
			best = c
		}
	}
	return best
}
