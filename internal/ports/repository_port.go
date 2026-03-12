package ports

import (
	"context"

	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// QuoteRepository is the outbound port for persisting and retrieving quotes.
// All implementations must be safe for concurrent use.
// A nil QuoteRepository is explicitly allowed — callers must guard with a nil
// check and skip persistence when no repo is configured.
type QuoteRepository interface {
	// Save persists quote results for a fan-out, keyed by requestID.
	// Callers should use ON CONFLICT DO NOTHING semantics — duplicate saves
	// for the same (requestID, carrierID) pair must not return an error.
	Save(ctx context.Context, requestID string, results []domain.QuoteResult) error

	// FindByRequestID returns non-expired results for requestID.
	// ok is false when no valid cache entry exists (not found or all expired).
	// Implementations must filter out rows whose expires_at is in the past.
	FindByRequestID(ctx context.Context, requestID string) (results []domain.QuoteResult, ok bool, err error)

	// DeleteExpired removes all rows whose expires_at is in the past.
	// Returns the count of deleted rows.
	DeleteExpired(ctx context.Context) (int64, error)
}
