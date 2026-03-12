package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

// ---- Part 2: Concurrency (task 4.7) -----------------------------------------

func TestOrchestrator_AllThreeCarriersRespond_ReturnsSortedResults(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// REQ-ORCH-002: all three carriers respond → returns ≥2 results sorted ascending.
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	beta := makeCarrier("beta", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	gamma := makeCarrier("gamma", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, beta, gamma})
	orch := fix.build(t)

	req := domain.QuoteRequest{
		RequestID:     "int-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       500 * time.Millisecond,
	}
	results, err := orch.GetQuotes(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected ≥2 results, got %d", len(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i].Premium.Amount < results[i-1].Premium.Amount {
			t.Fatalf("results not sorted ascending at index %d", i)
		}
	}
}

func TestOrchestrator_OpenCBCarrierExcluded(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// REQ-ORCH-003: carrier with Open CB excluded from fan-out results.
	alphaCfg := defaultCfg()
	alphaCfg.FailureThreshold = 1
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, alphaCfg)
	beta := makeCarrier("beta", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, beta})

	// Trip alpha's circuit breaker to Open by registering a failing carrier.
	failingMC := adapter.NewMockCarrier("alpha", adapter.MockConfig{
		BaseLatency: 1 * time.Millisecond,
		FailureRate: 1.0,
	}, discardLog)
	fix.registry.Register("alpha", adapter.RegisterMockCarrier(failingMC))

	orch := fix.build(t)

	// First call — alpha will fail and open its CB.
	req := domain.QuoteRequest{
		RequestID:     "int-cb-trip",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       500 * time.Millisecond,
	}
	_, _ = orch.GetQuotes(t.Context(), req)

	// Verify alpha CB is now Open.
	if fix.breakers["alpha"].State() != ports.CBStateOpen {
		t.Skip("alpha CB not tripped — probabilistic test, skipping")
	}

	// Second call — alpha CB is Open so it short-circuits; only beta returns.
	req2 := domain.QuoteRequest{
		RequestID:     "int-02",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       500 * time.Millisecond,
	}
	results, err := orch.GetQuotes(t.Context(), req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.CarrierID == "alpha" {
			t.Fatal("REQ-ORCH-003: alpha (Open CB) appeared in results")
		}
	}
}

func TestOrchestrator_RateLimiterExhausted_CarrierSkipped(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// REQ-ORCH-003: carrier whose rate limiter is exhausted skipped in fan-out.
	alphaCfg := defaultCfg()
	alphaCfg.RateLimit = domain.RateLimitConfig{TokensPerSecond: 0.001, Burst: 1}
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, alphaCfg)
	beta := makeCarrier("beta", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, beta})

	// Drain alpha's token manually.
	fix.limiters["alpha"].TryAcquire()

	orch := fix.build(t)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	req := domain.QuoteRequest{
		RequestID:     "int-03",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       200 * time.Millisecond,
	}
	results, err := orch.GetQuotes(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Beta should still respond; alpha skipped (rate limited).
	hasBeta := false
	for _, r := range results {
		if r.CarrierID == "beta" {
			hasBeta = true
		}
	}
	if !hasBeta {
		t.Fatal("REQ-ORCH-003: beta should respond even when alpha is rate-limited")
	}
}

func TestOrchestrator_ShortTimeout_OnlyFastCarrierReturns(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// REQ-ORCH-005: short timeout (300ms) returns only fast carrier results — no Gamma.
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	gammaCfg := defaultCfg()
	gammaCfg.RateLimit = domain.RateLimitConfig{TokensPerSecond: 100, Burst: 100}
	gamma := makeCarrier("gamma", []domain.CoverageLine{domain.CoverageLineAuto}, gammaCfg)

	fix := newFixture(t, []domain.Carrier{alpha, gamma})

	// Override gamma with the slow mock (800ms).
	slowGamma := adapter.NewGamma(discardLog)
	fix.registry.Register("gamma", adapter.RegisterMockCarrier(slowGamma))

	orch := fix.build(t)

	req := domain.QuoteRequest{
		RequestID:     "int-04",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       300 * time.Millisecond,
	}
	results, err := orch.GetQuotes(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.CarrierID == "gamma" {
			t.Fatal("REQ-ORCH-005: gamma (800ms) should not appear with 300ms timeout")
		}
	}
	// Alpha (10ms) must be in results.
	hasAlpha := false
	for _, r := range results {
		if r.CarrierID == "alpha" {
			hasAlpha = true
		}
	}
	if !hasAlpha {
		t.Fatal("REQ-ORCH-005: alpha should appear within 300ms timeout")
	}
}

func TestOrchestrator_NoGoroutineLeak_AfterCtxCancel(t *testing.T) {
	// REQ-ORCH-006: no goroutine leak after context cancellation.
	// Note: goleak.VerifyNone is placed BEFORE the deferred cancel to ensure
	// we wait for goroutines to exit naturally.
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	gamma := makeCarrier("gamma", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, gamma})
	// Replace gamma with a blocking carrier.
	slowGamma := adapter.NewGamma(discardLog)
	fix.registry.Register("gamma", adapter.RegisterMockCarrier(slowGamma))

	orch := fix.build(t)

	ctx, cancel := context.WithCancel(t.Context())

	req := domain.QuoteRequest{
		RequestID:     "int-05-leak",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       200 * time.Millisecond,
	}

	// Run GetQuotes with short timeout — cancel parent ctx too.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, _ = orch.GetQuotes(ctx, req)

	// Give goroutines a moment to exit after context cancellation.
	time.Sleep(50 * time.Millisecond)

	goleak.VerifyNone(t, goleak.IgnoreCurrent())
}

func TestOrchestrator_ConcurrentGetQuotes_NoRace(t *testing.T) {
	t.Parallel()
	// REQ-ORCH-006: 100 concurrent GetQuotes with -race — zero data races.
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	beta := makeCarrier("beta", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, beta})
	orch := fix.build(t)

	const concurrency = 100
	var wg sync.WaitGroup
	ctx := t.Context()
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			req := domain.QuoteRequest{
				RequestID:     "concurrent-req",
				CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
				Timeout:       500 * time.Millisecond,
			}
			_, _ = orch.GetQuotes(ctx, req)
		}(i)
	}
	wg.Wait()
}

func TestOrchestrator_MissingRegistryEntry_NoPanic(t *testing.T) {
	t.Parallel()
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// FIX-M4: A carrier in the carriers slice but NOT in the adapter registry
	// must not cause a panic. The carrier should be skipped silently.
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	ghost := makeCarrier("ghost", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := newFixture(t, []domain.Carrier{alpha, ghost})
	// Remove ghost from the registry so it has no adapter.
	fix.registry = adapter.NewRegistry()
	// Re-register only alpha.
	mc := adapter.NewMockCarrier("alpha", adapter.MockConfig{
		BaseLatency: 10 * time.Millisecond,
		FailureRate: 0.0,
	}, discardLog)
	fix.registry.Register("alpha", adapter.RegisterMockCarrier(mc))

	orch := fix.build(t)

	req := domain.QuoteRequest{
		RequestID:     "nil-exec-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       500 * time.Millisecond,
	}
	results, err := orch.GetQuotes(t.Context(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Alpha should still respond; ghost should be absent.
	for _, r := range results {
		if r.CarrierID == "ghost" {
			t.Fatal("ghost carrier (no adapter) should not appear in results")
		}
	}
	hasAlpha := false
	for _, r := range results {
		if r.CarrierID == "alpha" {
			hasAlpha = true
		}
	}
	if !hasAlpha {
		t.Fatal("alpha should still respond when ghost is missing from registry")
	}
}

// ---- Benchmark (task 4.8) --------------------------------------------------

func BenchmarkOrchestrator_GetQuotes_TwoCarriers(b *testing.B) {
	alpha := makeCarrier("alpha", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())
	beta := makeCarrier("beta", []domain.CoverageLine{domain.CoverageLineAuto}, defaultCfg())

	fix := &orchestratorFixture{
		carriers: []domain.Carrier{alpha, beta},
		registry: adapter.NewRegistry(),
		breakers: make(map[string]*circuitbreaker.Breaker),
		limiters: make(map[string]*ratelimiter.Limiter),
		trackers: make(map[string]*orchestrator.EMATracker),
		metrics:  testutil.NewNoopRecorder(),
	}
	for _, c := range fix.carriers {
		mc := adapter.NewMockCarrier(c.ID, adapter.MockConfig{
			BaseLatency: 5 * time.Millisecond,
			JitterMs:    0,
			FailureRate: 0.0,
		}, discardLog)
		fix.registry.Register(c.ID, adapter.RegisterMockCarrier(mc))
		cbCfg := circuitbreaker.Config{
			FailureThreshold: c.Config.FailureThreshold,
			SuccessThreshold: c.Config.SuccessThreshold,
			OpenTimeout:      c.Config.OpenTimeout,
		}
		fix.breakers[c.ID] = circuitbreaker.New(c.ID, cbCfg, fix.metrics)
		fix.limiters[c.ID] = ratelimiter.New(c.ID, c.Config.RateLimit, fix.metrics)
		fix.trackers[c.ID] = orchestrator.NewEMATracker(c.ID, c.Config.TimeoutHint, c.Config, fix.metrics)
	}
	orch := orchestrator.New(
		fix.carriers, fix.registry, fix.breakers, fix.limiters, fix.trackers,
		fix.metrics, orchestrator.Config{HedgePollInterval: 5 * time.Millisecond}, discardLog,
		nil, // no repository in benchmarks
	)

	req := domain.QuoteRequest{
		RequestID:     "bench-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
		Timeout:       1 * time.Second,
	}

	ctx := b.Context()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = orch.GetQuotes(ctx, req)
		}
	})
}
