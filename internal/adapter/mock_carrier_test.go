// Package adapter_test tests mock carrier behaviour profiles: Alpha, Beta, Gamma.
// REQ-ADAPT-003
package adapter_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// devNull is a slog logger that discards all output; used in tests where log
// output would clutter the test run.
var devNull = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))

// makeRequest returns a minimal QuoteRequest for mock carrier tests.
func makeRequest(requestID string) adapter.MockRequest {
	return adapter.MockRequest{
		RequestID:     requestID,
		CoverageLines: []string{"auto"},
	}
}

func TestMockCarrier_AlphaReturnsWithinLatencyBound(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-003: Alpha returns a valid result within 100ms (P99).
	// Alpha config: BaseLatency=50ms, JitterMs=10, FailureRate=0.0
	// Worst case latency = 50 + 10 = 60ms — well under 100ms.
	mc := adapter.NewAlpha(devNull)
	ctx := context.Background()

	start := time.Now()
	resp, err := mc.Call(ctx, makeRequest("alpha-001"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("REQ-ADAPT-003 Alpha: unexpected error: %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("REQ-ADAPT-003 Alpha: latency %v exceeds 100ms P99 bound", elapsed)
	}
	if resp.CarrierID != "alpha" {
		t.Fatalf("REQ-ADAPT-003 Alpha: CarrierID = %q, want alpha", resp.CarrierID)
	}
}

func TestMockCarrier_BetaFailureRateApproximatelyTenPercent(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-003: Beta returns ErrCarrierUnavailable at ~10% failure rate.
	// Test with 100 calls; assert 5%–20% fail.
	mc := adapter.NewBeta(devNull)
	ctx := context.Background()

	const calls = 100
	failures := 0

	for i := 0; i < calls; i++ {
		_, err := mc.Call(ctx, makeRequest("beta-rate-test"))
		if err != nil {
			if err == domain.ErrCarrierUnavailable {
				failures++
			} else {
				t.Fatalf("REQ-ADAPT-003 Beta: unexpected error (not ErrCarrierUnavailable): %v", err)
			}
		}
	}

	rate := float64(failures) / float64(calls)
	if rate < 0.05 || rate > 0.20 {
		t.Fatalf("REQ-ADAPT-003 Beta: failure rate %.2f not in [0.05, 0.20] over %d calls", rate, calls)
	}
}

func TestMockCarrier_GammaReturnsInLatencyWindow(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-003: Gamma returns a valid result with latency in [750ms, 950ms].
	// Gamma config: BaseLatency=800ms, JitterMs=50, FailureRate=0.0
	// Valid range: [750ms, 850ms] nominal; use 700ms–950ms to handle system jitter.
	mc := adapter.NewGamma(devNull)
	ctx := context.Background()

	start := time.Now()
	resp, err := mc.Call(ctx, makeRequest("gamma-001"))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("REQ-ADAPT-003 Gamma: unexpected error: %v", err)
	}
	// Lower bound: 800ms - 50ms jitter - 50ms system slack = 700ms
	// Upper bound: 800ms + 50ms jitter + 100ms system slack = 950ms
	const lower = 700 * time.Millisecond
	const upper = 950 * time.Millisecond
	if elapsed < lower || elapsed > upper {
		t.Fatalf("REQ-ADAPT-003 Gamma: latency %v not in [%v, %v]", elapsed, lower, upper)
	}
	if resp.CarrierID != "gamma" {
		t.Fatalf("REQ-ADAPT-003 Gamma: CarrierID = %q, want gamma", resp.CarrierID)
	}
}

func TestMockCarrier_CtxCancellationExitsWithin10ms(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-003: any mock carrier exits within 10ms of context cancellation.
	// We cancel at 100ms while Gamma's nominal latency is 800ms.
	tests := []struct {
		name    string
		carrier *adapter.MockCarrier
	}{
		{name: "REQ-ADAPT-003 alpha ctx cancel", carrier: adapter.NewAlpha(devNull)},
		{name: "REQ-ADAPT-003 beta ctx cancel", carrier: adapter.NewBeta(devNull)},
		{name: "REQ-ADAPT-003 gamma ctx cancel", carrier: adapter.NewGamma(devNull)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			start := time.Now()
			_, err := tc.carrier.Call(ctx, makeRequest("cancel-test"))
			elapsed := time.Since(start)

			// For fast carriers (Alpha, Beta), the call may succeed before
			// cancellation — that is acceptable. For Gamma, it must cancel.
			// Regardless of outcome: if ctx was cancelled, elapsed must be
			// ≤ 110ms (cancel deadline + 10ms grace).
			_ = err // may be nil (fast carrier) or ctx.Err() (slow carrier)
			if elapsed > 110*time.Millisecond {
				t.Fatalf("%s: took %v after ctx cancel (want ≤110ms)", tc.name, elapsed)
			}
		})
	}
}

func TestMockCarrier_GammaCtxCancellationReturnsError(t *testing.T) {
	t.Parallel()

	// Gamma (800ms) will always be cancelled by a 100ms context.
	mc := adapter.NewGamma(devNull)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := mc.Call(ctx, makeRequest("gamma-cancel"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("REQ-ADAPT-003 Gamma ctx cancel: expected error, got nil")
	}
	if elapsed > 110*time.Millisecond {
		t.Fatalf("REQ-ADAPT-003 Gamma ctx cancel: elapsed %v exceeds 110ms grace window", elapsed)
	}
}
