package ratelimiter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

func newLimiter(t *testing.T, tps float64, burst int) (*ratelimiter.Limiter, *testutil.NoopRecorder) {
	t.Helper()
	rec := testutil.NewNoopRecorder()
	cfg := domain.RateLimitConfig{TokensPerSecond: tps, Burst: burst}
	return ratelimiter.New("test-carrier", cfg, rec), rec
}

func TestLimiter_WaitAcquiresTokenWhenBucketHasCapacity(t *testing.T) {
	t.Parallel()

	l, _ := newLimiter(t, 100, 10)
	ctx := context.Background()

	if err := l.Wait(ctx); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestLimiter_TryAcquireReturnsFalseWhenBucketEmpty(t *testing.T) {
	t.Parallel()

	// Burst=1, TPS=0.1 (very slow refill). Drain the single token first.
	l, _ := newLimiter(t, 0.1, 1)
	if !l.TryAcquire() {
		t.Fatal("first TryAcquire should succeed (burst=1)")
	}
	// Now the bucket is empty — should return false without blocking.
	start := time.Now()
	if l.TryAcquire() {
		t.Fatal("expected false when bucket is empty")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Fatalf("TryAcquire blocked for %v — must be non-blocking", elapsed)
	}
}

func TestLimiter_WaitReturnsErrRateLimitExceededOnCtxCancel(t *testing.T) {
	t.Parallel()

	// TPS=0.001 and Burst=1 — drain the burst token, then Wait will block.
	l, rec := newLimiter(t, 0.001, 1)
	// Drain the one burst token.
	_ = l.TryAcquire()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately.
	cancel()

	err := l.Wait(ctx)
	if !errors.Is(err, domain.ErrRateLimitExceeded) {
		t.Fatalf("expected ErrRateLimitExceeded, got %v", err)
	}
	if rec.RecordRateLimitRejectionCount.Load() != 1 {
		t.Fatalf("expected 1 rejection metric, got %d", rec.RecordRateLimitRejectionCount.Load())
	}
}

func TestLimiter_PerCarrierIsolation(t *testing.T) {
	t.Parallel()

	recA := testutil.NewNoopRecorder()
	recB := testutil.NewNoopRecorder()
	cfgA := domain.RateLimitConfig{TokensPerSecond: 0.001, Burst: 1}
	cfgB := domain.RateLimitConfig{TokensPerSecond: 100, Burst: 10}

	la := ratelimiter.New("alpha", cfgA, recA)
	lb := ratelimiter.New("beta", cfgB, recB)

	// Drain alpha's token.
	la.TryAcquire()

	// Exhausting alpha must not affect beta.
	ctx := context.Background()
	if err := lb.Wait(ctx); err != nil {
		t.Fatalf("beta Wait should succeed, got %v", err)
	}
}

func TestLimiter_TokenReplenishmentAllowsTryAcquire(t *testing.T) {
	t.Parallel()

	// 100 TPS, burst 1. Drain the burst token.
	l, _ := newLimiter(t, 100, 1)
	if !l.TryAcquire() {
		t.Fatal("initial TryAcquire should succeed")
	}

	// After 1/rate = 10ms, a new token should be available.
	time.Sleep(15 * time.Millisecond)

	if !l.TryAcquire() {
		t.Fatal("TryAcquire should succeed after token replenishment")
	}
}
