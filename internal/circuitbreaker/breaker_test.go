package circuitbreaker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

var errFake = errors.New("fake carrier error")

// newBreaker is a test helper that builds a Breaker with explicit thresholds
// and returns the NoopRecorder for assertion.
func newBreaker(t *testing.T, failThreshold, successThreshold int, openTimeout time.Duration) (*circuitbreaker.Breaker, *testutil.NoopRecorder) {
	t.Helper()
	rec := testutil.NewNoopRecorder()
	cfg := circuitbreaker.Config{
		FailureThreshold: failThreshold,
		SuccessThreshold: successThreshold,
		OpenTimeout:      openTimeout,
	}
	return circuitbreaker.New("test-carrier", cfg, rec), rec
}

func TestBreaker_ClosedToOpenOnFailureThreshold(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 3, 1, time.Second)
	ctx := context.Background()
	fn := func() error { return errFake }

	for i := 0; i < 2; i++ {
		err := b.Execute(ctx, fn)
		if !errors.Is(err, errFake) {
			t.Fatalf("iteration %d: expected errFake, got %v", i, err)
		}
		if b.State() != ports.CBStateClosed {
			t.Fatalf("iteration %d: expected Closed, got %v", i, b.State())
		}
	}

	// Third failure should trip to Open.
	err := b.Execute(ctx, fn)
	if !errors.Is(err, errFake) {
		t.Fatalf("expected errFake on trip, got %v", err)
	}
	if b.State() != ports.CBStateOpen {
		t.Fatalf("expected Open after threshold, got %v", b.State())
	}
}

func TestBreaker_SuccessResetsFailureCounter(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 3, 1, time.Second)
	ctx := context.Background()
	failFn := func() error { return errFake }
	okFn := func() error { return nil }

	// Two failures then a success — should remain Closed.
	_ = b.Execute(ctx, failFn)
	_ = b.Execute(ctx, failFn)
	_ = b.Execute(ctx, okFn)

	// A third failure after the reset should NOT trip.
	_ = b.Execute(ctx, failFn)

	if b.State() != ports.CBStateClosed {
		t.Fatalf("expected Closed (counter reset), got %v", b.State())
	}
}

func TestBreaker_OpenShortCircuits(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 1, 1, time.Hour) // long timeout keeps it Open
	ctx := context.Background()
	callCount := 0
	fn := func() error {
		callCount++
		return errFake
	}

	// Trip to Open.
	_ = b.Execute(ctx, fn)
	if b.State() != ports.CBStateOpen {
		t.Fatal("expected Open")
	}

	callsBefore := callCount
	start := time.Now()
	err := b.Execute(ctx, fn)
	elapsed := time.Since(start)

	if !errors.Is(err, domain.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if callCount != callsBefore {
		t.Fatalf("fn must not be called while Open")
	}
	// Short-circuit should return in well under 1 µs on any real system.
	// We use a generous 1 ms to avoid flakiness on loaded CI.
	if elapsed > time.Millisecond {
		t.Fatalf("short-circuit took too long: %v", elapsed)
	}
}

func TestBreaker_OpenToHalfOpenAfterTimeout(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 1, 1, 20*time.Millisecond)
	ctx := context.Background()
	failFn := func() error { return errFake }

	// Trip to Open.
	_ = b.Execute(ctx, failFn)

	// Wait for OpenTimeout to elapse.
	time.Sleep(30 * time.Millisecond)

	// Next Execute should transition to HalfOpen and attempt the probe.
	okFn := func() error { return nil }
	err := b.Execute(ctx, okFn)
	if err != nil {
		t.Fatalf("expected nil from probe, got %v", err)
	}
	// After one success with SuccessThreshold=1, circuit should be Closed.
	if b.State() != ports.CBStateClosed {
		t.Fatalf("expected Closed after successful probe, got %v", b.State())
	}
}

func TestBreaker_HalfOpenToClosedOnSuccessThreshold(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 1, 2, 20*time.Millisecond)
	ctx := context.Background()

	// Trip to Open.
	_ = b.Execute(ctx, func() error { return errFake })
	time.Sleep(30 * time.Millisecond)

	// First probe — succeeds but SuccessThreshold=2, so stays HalfOpen.
	_ = b.Execute(ctx, func() error { return nil })

	if b.State() == ports.CBStateClosed {
		t.Fatal("should still be HalfOpen after 1 success (threshold=2)")
	}

	// Second probe — closes.
	err := b.Execute(ctx, func() error { return nil })
	if err != nil {
		t.Fatalf("expected nil on second probe, got %v", err)
	}
	if b.State() != ports.CBStateClosed {
		t.Fatalf("expected Closed after SuccessThreshold successes, got %v", b.State())
	}
}

func TestBreaker_HalfOpenToOpenOnProbeFailure(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 1, 1, 20*time.Millisecond)
	ctx := context.Background()

	// Trip to Open.
	_ = b.Execute(ctx, func() error { return errFake })
	time.Sleep(30 * time.Millisecond)

	// Probe fails — should re-open.
	err := b.Execute(ctx, func() error { return errFake })
	if !errors.Is(err, errFake) {
		t.Fatalf("expected errFake from failed probe, got %v", err)
	}
	if b.State() != ports.CBStateOpen {
		t.Fatalf("expected Open after probe failure, got %v", b.State())
	}
}

func TestBreaker_HalfOpenConcurrentProbeEnforcement(t *testing.T) {
	t.Parallel()

	// Very long OpenTimeout — we'll manually advance to HalfOpen by letting
	// a small timeout expire.
	b, _ := newBreaker(t, 1, 1, 20*time.Millisecond)
	ctx := context.Background()

	// Trip to Open.
	_ = b.Execute(ctx, func() error { return errFake })
	time.Sleep(30 * time.Millisecond)

	// Launch N goroutines that all try to Execute simultaneously.
	// Exactly 1 should call fn; the rest should receive ErrCircuitOpen.
	const N = 20
	var (
		mu        sync.Mutex
		callCount int
		cbOpenErr int
	)
	started := make(chan struct{})
	var wg sync.WaitGroup

	// Slow probe function that blocks until released.
	release := make(chan struct{})
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-started
			err := b.Execute(ctx, func() error {
				mu.Lock()
				callCount++
				mu.Unlock()
				<-release
				return nil
			})
			if errors.Is(err, domain.ErrCircuitOpen) {
				mu.Lock()
				cbOpenErr++
				mu.Unlock()
			}
		}()
	}

	close(started)
	time.Sleep(10 * time.Millisecond) // let goroutines reach Execute
	close(release)
	wg.Wait()

	if callCount != 1 {
		t.Fatalf("expected exactly 1 probe call, got %d", callCount)
	}
	if cbOpenErr != N-1 {
		t.Fatalf("expected %d ErrCircuitOpen, got %d", N-1, cbOpenErr)
	}
}

func TestBreaker_PrometheusGaugeEmittedOnTransition(t *testing.T) {
	t.Parallel()

	b, rec := newBreaker(t, 1, 1, 20*time.Millisecond)
	ctx := context.Background()

	initialSetCB := rec.SetCBStateCount.Load()
	initialTransitions := rec.RecordCBTransitionCount.Load()

	// Trip Closed → Open.
	_ = b.Execute(ctx, func() error { return errFake })
	if rec.SetCBStateCount.Load() <= initialSetCB {
		t.Fatal("SetCBState not emitted on Closed→Open transition")
	}
	if rec.RecordCBTransitionCount.Load() <= initialTransitions {
		t.Fatal("RecordCBTransition not emitted on Closed→Open transition")
	}

	// Wait for Open → HalfOpen.
	time.Sleep(30 * time.Millisecond)
	beforeHO := rec.SetCBStateCount.Load()
	beforeHOTrans := rec.RecordCBTransitionCount.Load()

	_ = b.Execute(ctx, func() error { return nil }) // probe succeeds
	if rec.SetCBStateCount.Load() <= beforeHO {
		t.Fatal("SetCBState not emitted on Open→HalfOpen and HalfOpen→Closed transitions")
	}
	if rec.RecordCBTransitionCount.Load() <= beforeHOTrans {
		t.Fatal("RecordCBTransition not emitted on Open→HalfOpen and HalfOpen→Closed transitions")
	}
}

func TestBreaker_ConcurrentExecute_NoRace(t *testing.T) {
	t.Parallel()

	b, _ := newBreaker(t, 5, 2, 10*time.Millisecond)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				_ = b.Execute(ctx, func() error { return errFake })
			} else {
				_ = b.Execute(ctx, func() error { return nil })
			}
		}(i)
	}
	wg.Wait()
}
