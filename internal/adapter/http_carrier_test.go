package adapter_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/domain"
)

var testLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// mockDeltaServer returns a DeltaResponse with fixed values.
func mockDeltaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adapter.DeltaResponse{ //nolint:errcheck
			QuoteID:         "delta-test-001",
			PremiumCents:    120_000,
			ValidForSeconds: 300,
		})
	}))
}

func TestHTTPCarrier_SuccessfulQuote(t *testing.T) {
	t.Parallel()

	srv := mockDeltaServer(t)
	defer srv.Close()

	carrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
		BaseURL:    srv.URL,
		MaxRetries: 1,
		RetryDelay: 10 * time.Millisecond,
		Timeout:    2 * time.Second,
	}, testLog)

	fn := adapter.RegisterDeltaCarrier(carrier)
	result, err := fn(t.Context(), domain.QuoteRequest{
		RequestID:     "test-req-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.CarrierID != "delta" {
		t.Errorf("carrier_id: want %q, got %q", "delta", result.CarrierID)
	}
	if result.Premium.Amount != 120_000 {
		t.Errorf("premium_cents: want 120000, got %d", result.Premium.Amount)
	}
	if result.Premium.Currency != "USD" {
		t.Errorf("currency: want USD, got %q", result.Premium.Currency)
	}
	if result.ExpiresAt.Before(time.Now().Add(4 * time.Minute)) {
		t.Errorf("expires_at should be ~5 minutes from now, got %v", result.ExpiresAt)
	}
}

func TestHTTPCarrier_RetryOn5xx(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			// First two attempts return 503.
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		// Third attempt succeeds.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adapter.DeltaResponse{ //nolint:errcheck
			QuoteID:         "retry-test",
			PremiumCents:    80_000,
			ValidForSeconds: 300,
		})
	}))
	defer srv.Close()

	carrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
		BaseURL:    srv.URL,
		MaxRetries: 3,
		RetryDelay: 5 * time.Millisecond,
		Timeout:    2 * time.Second,
	}, testLog)

	fn := adapter.RegisterDeltaCarrier(carrier)
	result, err := fn(t.Context(), domain.QuoteRequest{
		RequestID:     "retry-req-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
	})

	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if result.Premium.Amount != 80_000 {
		t.Errorf("premium_cents: want 80000, got %d", result.Premium.Amount)
	}
	if n := callCount.Load(); n != 3 {
		t.Errorf("expected exactly 3 HTTP calls (2 failures + 1 success), got %d", n)
	}
}

func TestHTTPCarrier_NoRetryOn4xx(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	carrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
		BaseURL:    srv.URL,
		MaxRetries: 3,
		RetryDelay: 5 * time.Millisecond,
		Timeout:    2 * time.Second,
	}, testLog)

	fn := adapter.RegisterDeltaCarrier(carrier)
	_, err := fn(t.Context(), domain.QuoteRequest{
		RequestID:     "noretry-req-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
	})

	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	// Must not retry 4xx — exactly one call.
	if n := callCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 HTTP call for 4xx (no retry), got %d", n)
	}
}

func TestHTTPCarrier_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow carrier.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	carrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
		BaseURL:    srv.URL,
		MaxRetries: 1,
		RetryDelay: 5 * time.Millisecond,
		Timeout:    10 * time.Second, // per-attempt timeout is long
	}, testLog)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	fn := adapter.RegisterDeltaCarrier(carrier)
	_, err := fn(ctx, domain.QuoteRequest{
		RequestID:     "cancel-req-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
	})

	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
}

func TestHTTPCarrier_AllAttemptsExhausted(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	carrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
		BaseURL:    srv.URL,
		MaxRetries: 2,
		RetryDelay: 5 * time.Millisecond,
		Timeout:    1 * time.Second,
	}, testLog)

	fn := adapter.RegisterDeltaCarrier(carrier)
	_, err := fn(t.Context(), domain.QuoteRequest{
		RequestID:     "exhaust-req-01",
		CoverageLines: []domain.CoverageLine{domain.CoverageLineAuto},
	})

	if err == nil {
		t.Fatal("expected error when all attempts exhausted, got nil")
	}
}
