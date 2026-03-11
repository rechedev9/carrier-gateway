// Package adapter implements the generic carrier adapter contract and the
// adapter registry. It also provides mock carrier implementations used for
// local development and testing.
//
// Type erasure pattern: concrete Adapter[Req, Resp] implementations are
// wrapped by the Register generic function into AdapterFunc closures that
// capture the concrete types. The Registry stores these closures keyed by
// carrier ID, making it possible to dispatch to any carrier without reflection.
package adapter

import (
	"context"

	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// Adapter transforms carrier-native request/response types to and from the
// domain types used by the orchestrator.
//
// Req is the carrier-native request type.
// Resp is the carrier-native response type.
type Adapter[Req, Resp any] interface {
	// ToCarrierRequest converts a domain QuoteRequest to a carrier-native request.
	ToCarrierRequest(ctx context.Context, q domain.QuoteRequest) (Req, error)
	// FromCarrierResponse converts a carrier-native response to a domain QuoteResult.
	FromCarrierResponse(ctx context.Context, r Resp, carrierID string) (domain.QuoteResult, error)
}

// AdapterFunc is the type-erased form of an Adapter, stored in the Registry.
// It is a closure that captures the concrete Adapter[Req, Resp] and handles
// the full conversion pipeline (ToCarrierRequest → carrier call →
// FromCarrierResponse) without reflection.
type AdapterFunc func(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)

// Register creates an AdapterFunc closure that captures adapter and carrier,
// and executes the full conversion pipeline on each call.
//
// Type parameters:
//   - Req: carrier-native request type
//   - Resp: carrier-native response type
//
// Parameters:
//   - a: the Adapter[Req, Resp] that converts domain ↔ carrier types.
//   - carrier: the underlying carrier implementation exposing a Call method.
//   - carrierID: the stable carrier identifier used in QuoteResult.CarrierID.
func Register[Req, Resp any](
	a Adapter[Req, Resp],
	carrier interface {
		Call(ctx context.Context, req Req) (Resp, error)
	},
	carrierID string,
) AdapterFunc {
	return func(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error) {
		nativeReq, err := a.ToCarrierRequest(ctx, req)
		if err != nil {
			return domain.QuoteResult{}, err
		}
		nativeResp, err := carrier.Call(ctx, nativeReq)
		if err != nil {
			return domain.QuoteResult{}, err
		}
		return a.FromCarrierResponse(ctx, nativeResp, carrierID)
	}
}

// Registry maps carrier IDs to type-erased AdapterFuncs.
// It is built once at startup and never mutated after that — safe for
// concurrent reads without a lock.
type Registry struct {
	adapters map[string]AdapterFunc
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]AdapterFunc),
	}
}

// Register registers an AdapterFunc under the given carrier ID.
// Register must be called before the Registry is shared across goroutines.
func (r *Registry) Register(carrierID string, fn AdapterFunc) {
	r.adapters[carrierID] = fn
}

// Get returns the AdapterFunc for a carrier ID.
// Returns (nil, false) if no adapter is registered for that ID.
func (r *Registry) Get(carrierID string) (AdapterFunc, bool) {
	fn, ok := r.adapters[carrierID]
	return fn, ok
}
