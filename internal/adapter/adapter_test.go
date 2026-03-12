// Package adapter_test tests the generic Registry and type-erased AdapterFunc.
// REQ-ADAPT-001, REQ-ADAPT-002, REQ-ADAPT-004
package adapter_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// newRegistry builds a Registry pre-populated with real MockCarrier adapters
// for the given carrier IDs.
func newRegistryWithMocks(t *testing.T, ids ...string) *adapter.Registry {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := adapter.NewRegistry()
	for _, id := range ids {
		mc := adapter.NewMockCarrier(id, adapter.MockConfig{
			BaseLatency: 5 * time.Millisecond,
			JitterMs:    0,
			FailureRate: 0.0,
		}, log)
		reg.Register(id, adapter.RegisterMockCarrier(mc))
	}
	return reg
}

func TestRegistry_GetReturnsCorrectAdapterFunc(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-001: Registry.Get returns the AdapterFunc registered for the carrier ID.
	tests := []struct {
		name       string
		registerID string
		lookupID   string
		wantFound  bool
	}{
		{name: "REQ-ADAPT-001 existing carrier alpha", registerID: "alpha", lookupID: "alpha", wantFound: true},
		{name: "REQ-ADAPT-001 existing carrier beta", registerID: "beta", lookupID: "beta", wantFound: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := newRegistryWithMocks(t, tc.registerID)
			fn, ok := reg.Get(tc.lookupID)
			if ok != tc.wantFound {
				t.Fatalf("Get(%q) ok=%v, want %v", tc.lookupID, ok, tc.wantFound)
			}
			if tc.wantFound && fn == nil {
				t.Fatal("Get returned ok=true but fn is nil")
			}
		})
	}
}

func TestRegistry_AdapterFuncRoundTrip(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-002: type-erased AdapterFunc round-trips QuoteRequest → MockRequest → MockResponse → QuoteResult.
	// result.CarrierID must match the registered key.
	tests := []struct {
		name      string
		carrierID string
		requestID string
		coverage  []domain.CoverageLine
	}{
		{
			name:      "REQ-ADAPT-002 alpha round-trip",
			carrierID: "alpha",
			requestID: "req-001",
			coverage:  []domain.CoverageLine{domain.CoverageLineAuto},
		},
		{
			name:      "REQ-ADAPT-002 beta round-trip",
			carrierID: "beta",
			requestID: "req-002",
			coverage:  []domain.CoverageLine{domain.CoverageLineHomeowners},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := newRegistryWithMocks(t, tc.carrierID)
			fn, ok := reg.Get(tc.carrierID)
			if !ok {
				t.Fatalf("no adapter registered for %q", tc.carrierID)
			}

			req := domain.QuoteRequest{
				RequestID:     tc.requestID,
				CoverageLines: tc.coverage,
				Timeout:       500 * time.Millisecond,
			}

			result, err := fn(context.Background(), req)
			if err != nil {
				t.Fatalf("AdapterFunc returned error: %v", err)
			}
			if result.CarrierID != tc.carrierID {
				t.Fatalf("result.CarrierID = %q, want %q", result.CarrierID, tc.carrierID)
			}
			if result.Premium.Amount <= 0 {
				t.Fatalf("result.Premium.Amount should be positive, got %d", result.Premium.Amount)
			}
			if result.Premium.Currency != "USD" {
				t.Fatalf("result.Premium.Currency = %q, want USD", result.Premium.Currency)
			}
		})
	}
}

func TestRegistry_GetUnknownCarrierReturnsFalse(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-004: Registry.Get on unknown carrier returns (nil, false).
	reg := newRegistryWithMocks(t, "alpha")
	fn, ok := reg.Get("nonexistent")
	if ok {
		t.Fatal("Get on unknown carrier should return false")
	}
	if fn != nil {
		t.Fatal("Get on unknown carrier should return nil AdapterFunc")
	}
}

func TestRegistry_ConcurrentAdapterInvocations_NoContamination(t *testing.T) {
	t.Parallel()

	// REQ-ADAPT-004: two adapters with different internal state can be retrieved
	// and invoked concurrently without cross-contamination of results.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := adapter.NewRegistry()

	alphaCarrier := adapter.NewMockCarrier("alpha", adapter.MockConfig{
		BaseLatency: 5 * time.Millisecond,
		FailureRate: 0.0,
	}, log)
	betaCarrier := adapter.NewMockCarrier("beta", adapter.MockConfig{
		BaseLatency: 5 * time.Millisecond,
		FailureRate: 0.0,
	}, log)

	reg.Register("alpha", adapter.RegisterMockCarrier(alphaCarrier))
	reg.Register("beta", adapter.RegisterMockCarrier(betaCarrier))

	alphaFn, _ := reg.Get("alpha")
	betaFn, _ := reg.Get("beta")

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make([]error, goroutines*2)

	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		idx := i
		go func() {
			defer wg.Done()
			req := domain.QuoteRequest{
				RequestID:     "alpha-req",
				CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
				Timeout:       500 * time.Millisecond,
			}
			result, err := alphaFn(context.Background(), req)
			if err == nil && result.CarrierID != "alpha" {
				errs[idx*2] = errors.New("alpha result has wrong carrier_id: " + result.CarrierID)
			}
		}()
		go func() {
			defer wg.Done()
			req := domain.QuoteRequest{
				RequestID:     "beta-req",
				CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
				Timeout:       500 * time.Millisecond,
			}
			result, err := betaFn(context.Background(), req)
			if err == nil && result.CarrierID != "beta" {
				errs[idx*2+1] = errors.New("beta result has wrong carrier_id: " + result.CarrierID)
			}
		}()
	}
	wg.Wait()

	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
}
