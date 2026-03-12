package adapter

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/rechedev9/carrier-gateway/internal/domain"
)

// MockRequest is the carrier-native request type for mock carriers.
// It is intentionally minimal — its purpose is to demonstrate the
// type-erased adapter pattern with a concrete Req type parameter.
type MockRequest struct {
	// RequestID is the originating QuoteRequest.RequestID.
	RequestID string
	// CoverageLines is the set of lines requested, as plain strings.
	CoverageLines []string
}

// MockResponse is the carrier-native response type for mock carriers.
type MockResponse struct {
	// CarrierID is the unique identifier of the carrier that produced the response.
	CarrierID string
	// PremiumCents is the total quoted premium in cents (USD).
	PremiumCents int64
	// ExpiresInSeconds is the number of seconds until the quote expires.
	ExpiresInSeconds int
}

// MockConfig holds the simulation parameters for a MockCarrier.
type MockConfig struct {
	// BaseLatency is the nominal response latency.
	BaseLatency time.Duration
	// JitterMs is the maximum random jitter in milliseconds added to BaseLatency.
	// The actual jitter is uniformly distributed in [−JitterMs, +JitterMs].
	JitterMs int
	// FailureRate is the probability (0.0–1.0) of returning domain.ErrCarrierUnavailable.
	FailureRate float64
}

// MockCarrier simulates a carrier with configurable latency and failure injection.
// All methods are safe for concurrent use.
type MockCarrier struct {
	id  string
	cfg MockConfig
	log *slog.Logger
}

// NewMockCarrier returns a MockCarrier with the given id and simulation config.
func NewMockCarrier(id string, cfg MockConfig, log *slog.Logger) *MockCarrier {
	return &MockCarrier{id: id, cfg: cfg, log: log}
}

// NewAlpha returns a MockCarrier simulating a fast, reliable carrier.
// BaseLatency=50ms, JitterMs=10, FailureRate=0.0.
func NewAlpha(log *slog.Logger) *MockCarrier {
	return NewMockCarrier("alpha", MockConfig{
		BaseLatency: 50 * time.Millisecond,
		JitterMs:    10,
		FailureRate: 0.0,
	}, log)
}

// NewBeta returns a MockCarrier simulating a medium-speed carrier with a 10%
// failure rate. BaseLatency=200ms, JitterMs=20, FailureRate=0.1.
func NewBeta(log *slog.Logger) *MockCarrier {
	return NewMockCarrier("beta", MockConfig{
		BaseLatency: 200 * time.Millisecond,
		JitterMs:    20,
		FailureRate: 0.1,
	}, log)
}

// NewGamma returns a MockCarrier simulating a slow but reliable carrier.
// BaseLatency=800ms, JitterMs=50, FailureRate=0.0.
func NewGamma(log *slog.Logger) *MockCarrier {
	return NewMockCarrier("gamma", MockConfig{
		BaseLatency: 800 * time.Millisecond,
		JitterMs:    50,
		FailureRate: 0.0,
	}, log)
}

// Call simulates a carrier HTTP call: sleeps for BaseLatency ± jitter, then
// returns domain.ErrCarrierUnavailable with probability FailureRate.
//
// The sleep honours ctx cancellation — if ctx is Done before the sleep
// completes, Call returns ctx.Err() immediately.
func (m *MockCarrier) Call(ctx context.Context, req MockRequest) (MockResponse, error) {
	jitterMs := 0
	if m.cfg.JitterMs > 0 {
		jitterMs = rand.Intn(m.cfg.JitterMs*2+1) - m.cfg.JitterMs
	}
	latency := m.cfg.BaseLatency + time.Duration(jitterMs)*time.Millisecond
	if latency < 0 {
		latency = 0
	}

	timer := time.NewTimer(latency)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return MockResponse{}, ctx.Err()
	case <-timer.C:
	}

	if m.cfg.FailureRate > 0 && rand.Float64() < m.cfg.FailureRate {
		m.log.Info("mock carrier injecting failure",
			slog.String("carrier_id", m.id),
			slog.String("request_id", req.RequestID),
		)
		return MockResponse{}, domain.ErrCarrierUnavailable
	}

	// Produce a deterministic-ish premium based on carrier ID and request.
	// The exact value is not important — tests verify range/existence, not a
	// specific number.
	premiumBase := int64(len(m.id)) * 10_000
	resp := MockResponse{
		CarrierID:        m.id,
		PremiumCents:     premiumBase + int64(rand.Intn(50_000)),
		ExpiresInSeconds: 300,
	}

	m.log.Info("mock carrier responded",
		slog.String("carrier_id", m.id),
		slog.String("request_id", req.RequestID),
		slog.Int64("premium_cents", resp.PremiumCents),
	)
	return resp, nil
}

// mockAdapter implements Adapter[MockRequest, MockResponse] for a given
// MockCarrier. It is used with the Register generic function to produce a
// type-erased AdapterFunc.
type mockAdapter struct {
	carrierID string
}

// ToCarrierRequest converts a domain.QuoteRequest to a MockRequest.
func (a *mockAdapter) ToCarrierRequest(_ context.Context, q domain.QuoteRequest) (MockRequest, error) {
	lines := make([]string, len(q.CoverageLines))
	for i, l := range q.CoverageLines {
		lines[i] = string(l)
	}
	return MockRequest{
		RequestID:     q.RequestID,
		CoverageLines: lines,
	}, nil
}

// FromCarrierResponse converts a MockResponse to a domain.QuoteResult.
func (a *mockAdapter) FromCarrierResponse(_ context.Context, r MockResponse, carrierID string) (domain.QuoteResult, error) {
	return domain.QuoteResult{
		CarrierID: carrierID,
		Premium: domain.Money{
			Amount:   r.PremiumCents,
			Currency: "USD",
		},
		ExpiresAt: time.Now().Add(time.Duration(r.ExpiresInSeconds) * time.Second),
	}, nil
}

// RegisterMockCarrier is a convenience helper that wires a MockCarrier into the
// generic Register function and returns a ready AdapterFunc.
// This bridges the Adapter[MockRequest, MockResponse] interface to the
// MockCarrier.Call method without any reflection.
func RegisterMockCarrier(carrier *MockCarrier) AdapterFunc {
	return Register[MockRequest, MockResponse](
		&mockAdapter{carrierID: carrier.id},
		carrier,
		carrier.id,
	)
}
