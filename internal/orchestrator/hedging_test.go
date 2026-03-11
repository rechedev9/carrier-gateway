package orchestrator_test

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

// makeTracker is a test helper that builds an EMATracker with the given
// seed and CarrierConfig values.
func makeTracker(t *testing.T, seed time.Duration, alpha float64, warmup int, multiplier float64) *orchestrator.EMATracker {
	t.Helper()
	rec := testutil.NewNoopRecorder()
	cfg := domain.CarrierConfig{
		EMAAlpha:              alpha,
		EMAWarmupObservations: warmup,
		HedgeMultiplier:       multiplier,
	}
	return orchestrator.NewEMATracker("test-carrier", seed, cfg, rec)
}

func TestEMATracker_InitialisedToTwiceSeed(t *testing.T) {
	t.Parallel()

	seed := 100 * time.Millisecond
	tracker := makeTracker(t, seed, 0.1, 10, 1.5)

	// P95 should be seeded to 2 × 100ms = 200ms.
	wantP95 := float64(seed.Milliseconds()) * 2.0
	if got := tracker.P95(); got != wantP95 {
		t.Fatalf("expected P95 = %v ms, got %v ms", wantP95, got)
	}
}

func TestEMATracker_P95ConvergesAfterObservations(t *testing.T) {
	t.Parallel()

	seed := 200 * time.Millisecond
	// Use a high alpha for faster convergence in tests.
	tracker := makeTracker(t, seed, 0.3, 0, 1.5)

	target := 50 * time.Millisecond
	for i := 0; i < 30; i++ {
		tracker.Record(target)
	}

	got := tracker.P95()
	targetMs := float64(target.Milliseconds())
	// After 30 records with alpha=0.3, P95 should be within 5 ms of target.
	if math.Abs(got-targetMs) > 5.0 {
		t.Fatalf("P95 did not converge: got %v ms, want ~%v ms (±5ms)", got, targetMs)
	}
}

func TestEMATracker_HedgeThresholdReturnMaxFloat64DuringWarmup(t *testing.T) {
	t.Parallel()

	tracker := makeTracker(t, 100*time.Millisecond, 0.1, 10, 1.5)

	// No observations yet — should return MaxFloat64.
	got := tracker.HedgeThreshold()
	if got != math.MaxFloat64 {
		t.Fatalf("expected MaxFloat64 during warm-up, got %v", got)
	}

	// 9 observations — still in warm-up.
	for i := 0; i < 9; i++ {
		tracker.Record(50 * time.Millisecond)
	}
	got = tracker.HedgeThreshold()
	if got != math.MaxFloat64 {
		t.Fatalf("expected MaxFloat64 with %d observations (warmup=%d), got %v", 9, 10, got)
	}
}

func TestEMATracker_HedgeThresholdAfterWarmup(t *testing.T) {
	t.Parallel()

	const warmup = 5
	const multiplier = 1.5
	tracker := makeTracker(t, 100*time.Millisecond, 0.1, warmup, multiplier)

	for i := 0; i < warmup; i++ {
		tracker.Record(50 * time.Millisecond)
	}

	p95 := tracker.P95()
	wantThreshold := p95 * multiplier
	got := tracker.HedgeThreshold()
	if math.Abs(got-wantThreshold) > 0.001 {
		t.Fatalf("expected HedgeThreshold = %v, got %v", wantThreshold, got)
	}
}

func TestEMATracker_ConcurrentRecord_NoRace(t *testing.T) {
	t.Parallel()

	tracker := makeTracker(t, 100*time.Millisecond, 0.1, 10, 1.5)

	const goroutines = 10
	const iterations = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				tracker.Record(time.Duration(j) * time.Millisecond)
				_ = tracker.P95()
				_ = tracker.HedgeThreshold()
			}
		}()
	}
	wg.Wait()
}
