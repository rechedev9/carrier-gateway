// Package ports defines the inbound and outbound interface contracts for the
// carrier gateway. Port definitions import only domain types and stdlib —
// no infrastructure packages are permitted here.
package ports

import (
	"context"

	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// CarrierPort is the outbound port used by the orchestrator to request a quote
// from a single carrier. Implementations live in internal/adapter/.
// All implementations must be safe for concurrent use.
type CarrierPort interface {
	// Quote requests a quote from the carrier for the given request.
	// Returns domain.ErrCarrierUnavailable on transient carrier errors.
	// Returns domain.ErrCircuitOpen when the circuit breaker is open.
	// Implementations must honour ctx cancellation and deadline.
	Quote(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)
}

// OrchestratorPort is the inbound port for the HTTP handler to trigger a
// fan-out quote request across all eligible carriers.
// Implementations must be safe for concurrent use.
type OrchestratorPort interface {
	// GetQuotes fans out to all eligible carriers and returns results sorted
	// by premium ascending. Returns an empty slice when no carrier can service
	// the request (no error is returned in that case). A partial result set may
	// be returned if some carriers time out or fail — callers should treat the
	// slice as best-effort.
	GetQuotes(ctx context.Context, req domain.QuoteRequest) ([]domain.QuoteResult, error)
}
