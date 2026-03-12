package adapter

import (
	"context"
	"log/slog"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// DeltaRequest is the carrier-native request type for Delta Insurance.
type DeltaRequest struct {
	// RequestID is the caller's idempotency key.
	RequestID string `json:"request_id"`
	// Lines are the requested lines of business (e.g. ["auto", "homeowners"]).
	Lines []string `json:"lines"`
}

// DeltaResponse is the carrier-native response from Delta Insurance.
type DeltaResponse struct {
	// QuoteID is Delta's internal quote reference.
	QuoteID string `json:"quote_id"`
	// PremiumCents is the total annual premium in USD cents.
	PremiumCents int64 `json:"premium_cents"`
	// ValidForSeconds is how long the quote remains valid.
	ValidForSeconds int `json:"valid_for_seconds"`
}

// deltaHTTPClient is the interface expected by the generic Register function.
// HTTPCarrier.Post satisfies it when wrapped by deltaClient.
type deltaHTTPClient struct {
	carrier *HTTPCarrier
}

func (d *deltaHTTPClient) Call(ctx context.Context, req DeltaRequest) (DeltaResponse, error) {
	var resp DeltaResponse
	if err := d.carrier.Post(ctx, "/v1/quotes", req, &resp); err != nil {
		return DeltaResponse{}, err
	}
	return resp, nil
}

// deltaAdapter implements Adapter[DeltaRequest, DeltaResponse].
type deltaAdapter struct{}

// ToCarrierRequest maps a domain.QuoteRequest to a DeltaRequest.
func (a *deltaAdapter) ToCarrierRequest(_ context.Context, q domain.QuoteRequest) (DeltaRequest, error) {
	lines := make([]string, len(q.CoverageLines))
	for i, l := range q.CoverageLines {
		lines[i] = string(l)
	}
	return DeltaRequest{
		RequestID: q.RequestID,
		Lines:     lines,
	}, nil
}

// FromCarrierResponse maps a DeltaResponse to a domain.QuoteResult.
func (a *deltaAdapter) FromCarrierResponse(_ context.Context, r DeltaResponse, carrierID string) (domain.QuoteResult, error) {
	validFor := time.Duration(r.ValidForSeconds) * time.Second
	if validFor <= 0 {
		validFor = 5 * time.Minute // safe default
	}
	return domain.QuoteResult{
		CarrierID:  carrierID,
		CarrierRef: r.QuoteID,
		Premium: domain.Money{
			Amount:   r.PremiumCents,
			Currency: "USD",
		},
		ExpiresAt: time.Now().Add(validFor),
	}, nil
}

// NewDeltaCarrier constructs an HTTPCarrier pre-configured for Delta Insurance.
func NewDeltaCarrier(cfg HTTPCarrierConfig, log *slog.Logger) *HTTPCarrier {
	return NewHTTPCarrier("delta", cfg, log)
}

// RegisterDeltaCarrier wires a Delta HTTPCarrier into the adapter registry.
// It returns a type-erased AdapterFunc ready for Registry.Register("delta", ...).
func RegisterDeltaCarrier(carrier *HTTPCarrier) AdapterFunc {
	return Register[DeltaRequest, DeltaResponse](
		&deltaAdapter{},
		&deltaHTTPClient{carrier: carrier},
		"delta",
	)
}
