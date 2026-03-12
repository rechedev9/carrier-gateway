package orchestrator_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

// parseSeed returns a deterministic seed from RANDOM_SEED env var, or a
// time-based seed for exploratory runs.
func parseSeed(t *testing.T) uint64 {
	t.Helper()
	if s := os.Getenv("RANDOM_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("invalid RANDOM_SEED %q: %v", s, err)
		}
		return v
	}
	return uint64(time.Now().UnixNano())
}

// allCoverages is the universe of valid coverage lines.
var allCoverages = []domain.CoverageLine{
	domain.CoverageLineAuto,
	domain.CoverageLineHomeowners,
	domain.CoverageLineUmbrella,
}

// randomCoverages picks 1-3 random coverage lines from the universe.
func randomCoverages(rng *rand.Rand) []domain.CoverageLine {
	n := rng.IntN(3) + 1
	perm := rng.Perm(len(allCoverages))
	out := make([]domain.CoverageLine, n)
	for i := range n {
		out[i] = allCoverages[perm[i]]
	}
	return out
}

// randomCarriers generates 1-6 carriers with random configs.
func randomCarriers(rng *rand.Rand) []domain.Carrier {
	n := rng.IntN(6) + 1
	carriers := make([]domain.Carrier, n)
	for i := range n {
		id := fmt.Sprintf("carrier-%d", i)
		carriers[i] = domain.Carrier{
			ID:           id,
			Name:         id,
			Capabilities: randomCoverages(rng),
			Config: domain.CarrierConfig{
				TimeoutHint:           time.Duration(rng.IntN(491)+10) * time.Millisecond, // 10-500ms
				OpenTimeout:           30 * time.Second,
				FailureThreshold:      rng.IntN(10) + 1,
				SuccessThreshold:      rng.IntN(5) + 1,
				HedgeMultiplier:       1.0 + rng.Float64()*2.0, // 1.0-3.0
				EMAAlpha:              0.05 + rng.Float64()*0.3, // 0.05-0.35
				EMAWarmupObservations: rng.IntN(20),
				RateLimit: domain.RateLimitConfig{
					TokensPerSecond: 50 + rng.Float64()*200, // 50-250
					Burst:           rng.IntN(50) + 10,       // 10-59
				},
				Priority: i + 1,
			},
		}
	}
	return carriers
}

// buildRandomOrch creates a fully wired orchestrator from random carriers.
// If tripCB >= 0, trips that carrier's circuit breaker to Open.
func buildRandomOrch(t *testing.T, rng *rand.Rand, carriers []domain.Carrier, tripCB int) *orchestrator.Orchestrator {
	t.Helper()
	registry := adapter.NewRegistry()
	breakers := make(map[string]*circuitbreaker.Breaker, len(carriers))
	limiters := make(map[string]*ratelimiter.Limiter, len(carriers))
	trackers := make(map[string]*orchestrator.EMATracker, len(carriers))
	noop := testutil.NewNoopRecorder()

	for i, c := range carriers {
		mc := adapter.NewMockCarrier(c.ID, adapter.MockConfig{
			BaseLatency: time.Duration(rng.IntN(100)+1) * time.Millisecond,
			JitterMs:    rng.IntN(21),
			FailureRate: rng.Float64() * 0.5, // 0-50%
		}, discardLog)
		registry.Register(c.ID, adapter.RegisterMockCarrier(mc))

		cb := circuitbreaker.New(c.ID, circuitbreaker.Config{
			FailureThreshold: c.Config.FailureThreshold,
			SuccessThreshold: c.Config.SuccessThreshold,
			OpenTimeout:      c.Config.OpenTimeout,
		}, noop)
		if i == tripCB {
			// Force Open by executing failing functions up to threshold.
			failFn := func() error { return errors.New("forced failure") }
			for range c.Config.FailureThreshold {
				_ = cb.Execute(t.Context(), failFn)
			}
		}
		breakers[c.ID] = cb
		limiters[c.ID] = ratelimiter.New(c.ID, c.Config.RateLimit, noop)
		trackers[c.ID] = orchestrator.NewEMATracker(c.ID, c.Config.TimeoutHint, c.Config, noop)
	}

	return orchestrator.New(
		carriers, registry, breakers, limiters, trackers,
		noop, orchestrator.Config{HedgePollInterval: 5 * time.Millisecond}, discardLog,
		nil,
	)
}

// checkInvariants verifies the core orchestrator contract on a result set.
func checkInvariants(t *testing.T, results []domain.QuoteResult, pool map[string]bool, eligible int, label string) {
	t.Helper()

	// No duplicate carrier IDs.
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		if seen[r.CarrierID] {
			t.Fatalf("%s: duplicate carrier_id %q", label, r.CarrierID)
		}
		seen[r.CarrierID] = true
	}

	// Sorted by premium ascending.
	for i := 1; i < len(results); i++ {
		if results[i].Premium.Amount < results[i-1].Premium.Amount {
			t.Fatalf("%s: results not sorted at index %d: %d > %d",
				label, i, results[i-1].Premium.Amount, results[i].Premium.Amount)
		}
	}

	// Result count <= eligible carriers.
	if len(results) > eligible {
		t.Fatalf("%s: got %d results but only %d eligible carriers", label, len(results), eligible)
	}

	for _, r := range results {
		if r.CarrierID == "" {
			t.Fatalf("%s: empty carrier_id", label)
		}
		if r.Premium.Amount <= 0 {
			t.Fatalf("%s: carrier %q premium_cents=%d, want >0", label, r.CarrierID, r.Premium.Amount)
		}
		if r.Premium.Currency != "USD" {
			t.Fatalf("%s: carrier %q currency=%q, want USD", label, r.CarrierID, r.Premium.Currency)
		}
		if r.Latency < 0 {
			t.Fatalf("%s: carrier %q latency=%v, want >=0", label, r.CarrierID, r.Latency)
		}
		if !pool[r.CarrierID] {
			t.Fatalf("%s: carrier %q not in pool", label, r.CarrierID)
		}
	}
}

func TestOrchestrator_Random(t *testing.T) {
	t.Parallel()
	seed := parseSeed(t)
	t.Logf("seed=%d (replay: RANDOM_SEED=%d)", seed, seed)
	rng := rand.New(rand.NewPCG(seed, seed^0xDEADBEEF))

	const iterations = 200
	for i := range iterations {
		carriers := randomCarriers(rng)

		// Build carrier pool set.
		pool := make(map[string]bool, len(carriers))
		for _, c := range carriers {
			pool[c.ID] = true
		}

		// ~20% chance of tripping a random carrier's CB.
		tripCB := -1
		if rng.Float64() < 0.2 {
			tripCB = rng.IntN(len(carriers))
		}

		orch := buildRandomOrch(t, rng, carriers, tripCB)

		// Random request.
		reqLines := randomCoverages(rng)
		timeout := time.Duration(rng.IntN(1800)+200) * time.Millisecond // 200ms-2s

		req := domain.QuoteRequest{
			RequestID:     fmt.Sprintf("rand-%d", i),
			CoverageLines: reqLines,
			Timeout:       timeout,
		}

		// Count eligible: carrier must have at least one matching capability
		// and not have an open CB.
		eligible := 0
		for ci, c := range carriers {
			if ci == tripCB {
				continue // CB is open
			}
			for _, cap := range c.Capabilities {
				for _, rl := range reqLines {
					if cap == rl {
						eligible++
						goto nextCarrier
					}
				}
			}
		nextCarrier:
		}

		results, err := orch.GetQuotes(t.Context(), req)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}

		checkInvariants(t, results, pool, eligible, fmt.Sprintf("iter %d", i))
	}
}

func TestOrchestrator_Random_Concurrent(t *testing.T) {
	t.Parallel()
	// No goleak here — goroutines from the final iteration may still be
	// draining when the test returns. Leak detection is covered by
	// TestOrchestrator_NoGoroutineLeak_AfterCtxCancel.

	seed := parseSeed(t)
	t.Logf("seed=%d (replay: RANDOM_SEED=%d)", seed, seed)
	rng := rand.New(rand.NewPCG(seed, seed^0xCAFEBABE))

	const (
		iterations  = 50
		concurrency = 10
	)

	for i := range iterations {
		carriers := randomCarriers(rng)
		pool := make(map[string]bool, len(carriers))
		for _, c := range carriers {
			pool[c.ID] = true
		}

		tripCB := -1
		if rng.Float64() < 0.2 {
			tripCB = rng.IntN(len(carriers))
		}

		orch := buildRandomOrch(t, rng, carriers, tripCB)
		reqLines := randomCoverages(rng)
		timeout := time.Duration(rng.IntN(1800)+200) * time.Millisecond

		// Count eligible (same logic as single-threaded test).
		eligible := 0
		for ci, c := range carriers {
			if ci == tripCB {
				continue
			}
			for _, cap := range c.Capabilities {
				for _, rl := range reqLines {
					if cap == rl {
						eligible++
						goto nextC
					}
				}
			}
		nextC:
		}

		var wg sync.WaitGroup
		ctx := t.Context()
		wg.Add(concurrency)
		for g := range concurrency {
			go func(g int) {
				defer wg.Done()
				req := domain.QuoteRequest{
					RequestID:     fmt.Sprintf("rand-c-%d-%d", i, g),
					CoverageLines: reqLines,
					Timeout:       timeout,
				}
				results, err := orch.GetQuotes(ctx, req)
				if err != nil {
					t.Errorf("iter %d goroutine %d: unexpected error: %v", i, g, err)
					return
				}
				checkInvariants(t, results, pool, eligible, fmt.Sprintf("iter %d goroutine %d", i, g))
			}(g)
		}
		wg.Wait()
	}
}

// countOpenCBs is a compile-time check that ports.CBStateOpen is accessible.
var _ = ports.CBStateOpen
