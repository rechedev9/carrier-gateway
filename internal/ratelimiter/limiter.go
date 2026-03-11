// Package ratelimiter provides a per-carrier token-bucket rate limiter that
// wraps golang.org/x/time/rate. It exposes two acquisition methods:
//
//   - Wait(ctx) — blocking; emits a rejection metric on context cancellation.
//   - TryAcquire() — non-blocking; returns false silently when the bucket is empty.
//
// All methods are safe for concurrent use.
package ratelimiter

import (
	"context"
	"errors"

	"golang.org/x/time/rate"

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// Limiter is a per-carrier token-bucket rate limiter.
// The zero value is not valid — use New.
type Limiter struct {
	inner     *rate.Limiter
	carrierID string
	metrics   ports.MetricsRecorder
}

// New returns a Limiter configured from cfg.
// TokensPerSecond must be positive; Burst must be ≥ 1.
func New(carrierID string, cfg domain.RateLimitConfig, m ports.MetricsRecorder) *Limiter {
	return &Limiter{
		inner:     rate.NewLimiter(rate.Limit(cfg.TokensPerSecond), cfg.Burst),
		carrierID: carrierID,
		metrics:   m,
	}
}

// Wait blocks until a token is available or ctx is cancelled.
//
// If ctx is cancelled before a token is acquired, Wait returns
// domain.ErrRateLimitExceeded and emits a RecordRateLimitRejection metric.
func (l *Limiter) Wait(ctx context.Context) error {
	if err := l.inner.Wait(ctx); err != nil {
		// golang.org/x/time/rate returns context errors on cancellation/deadline.
		// We normalise all such cases to domain.ErrRateLimitExceeded.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			l.metrics.RecordRateLimitRejection(l.carrierID)
			return domain.ErrRateLimitExceeded
		}
		// Unexpected rate.Limiter error (e.g., token > burst) — propagate as-is.
		return err
	}
	return nil
}

// TryAcquire attempts to take a token without blocking.
//
// Returns true if a token was acquired; false if the bucket is empty.
// Does NOT emit a rejection metric on false — hedge suppression is silent.
func (l *Limiter) TryAcquire() bool {
	return l.inner.Allow()
}
