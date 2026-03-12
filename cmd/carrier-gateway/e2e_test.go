package main_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/handler"
	"github.com/rechedev9/carrier-gateway/internal/metrics"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
	"github.com/rechedev9/carrier-gateway/internal/testutil"
)

// startTestServer wires the full composition root (mirrors main.go) and returns
// an httptest.Server. No DB, no Delta carrier — self-contained.
// Accepts testing.TB so it works in both *testing.T and *testing.F contexts.
func startTestServer(tb testing.TB) *httptest.Server {
	tb.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	rec := metrics.New(reg)

	carriers := []domain.Carrier{
		{
			ID:           "alpha",
			Name:         "Alpha Insurance",
			Capabilities: []domain.CoverageLine{domain.CoverageLineAuto, domain.CoverageLineHomeowners},
			Config: domain.CarrierConfig{
				TimeoutHint:           100 * time.Millisecond,
				FailureThreshold:      5,
				SuccessThreshold:      2,
				OpenTimeout:           30 * time.Second,
				HedgeMultiplier:       1.5,
				EMAAlpha:              0.1,
				EMAWindowSize:         19,
				EMAWarmupObservations: 10,
				Priority:              1,
				RateLimit:             domain.RateLimitConfig{TokensPerSecond: 100, Burst: 10},
			},
		},
		{
			ID:           "beta",
			Name:         "Beta Insurance",
			Capabilities: []domain.CoverageLine{domain.CoverageLineAuto, domain.CoverageLineUmbrella},
			Config: domain.CarrierConfig{
				TimeoutHint:           400 * time.Millisecond,
				FailureThreshold:      5,
				SuccessThreshold:      2,
				OpenTimeout:           30 * time.Second,
				HedgeMultiplier:       1.5,
				EMAAlpha:              0.1,
				EMAWindowSize:         19,
				EMAWarmupObservations: 10,
				Priority:              2,
				RateLimit:             domain.RateLimitConfig{TokensPerSecond: 50, Burst: 5},
			},
		},
		{
			ID:           "gamma",
			Name:         "Gamma Insurance",
			Capabilities: []domain.CoverageLine{domain.CoverageLineAuto, domain.CoverageLineHomeowners, domain.CoverageLineUmbrella},
			Config: domain.CarrierConfig{
				TimeoutHint:           1600 * time.Millisecond,
				FailureThreshold:      5,
				SuccessThreshold:      2,
				OpenTimeout:           30 * time.Second,
				HedgeMultiplier:       1.5,
				EMAAlpha:              0.1,
				EMAWindowSize:         19,
				EMAWarmupObservations: 10,
				Priority:              3,
				RateLimit:             domain.RateLimitConfig{TokensPerSecond: 20, Burst: 2},
			},
		},
	}

	// Mock carrier instances (same as main.go).
	alphaCarrier := adapter.NewAlpha(log)
	betaCarrier := adapter.NewBeta(log)
	gammaCarrier := adapter.NewGamma(log)

	registry := adapter.NewRegistry()
	registry.Register("alpha", adapter.RegisterMockCarrier(alphaCarrier))
	registry.Register("beta", adapter.RegisterMockCarrier(betaCarrier))
	registry.Register("gamma", adapter.RegisterMockCarrier(gammaCarrier))

	breakers := make(map[string]*circuitbreaker.Breaker, len(carriers))
	limiters := make(map[string]*ratelimiter.Limiter, len(carriers))
	trackers := make(map[string]*orchestrator.EMATracker, len(carriers))

	// Use a separate NoopRecorder for breakers/limiters/trackers to avoid
	// double-registering metrics on the Prometheus registry.
	noop := testutil.NewNoopRecorder()
	for _, c := range carriers {
		breakers[c.ID] = circuitbreaker.New(c.ID, circuitbreaker.Config{
			FailureThreshold: c.Config.FailureThreshold,
			SuccessThreshold: c.Config.SuccessThreshold,
			OpenTimeout:      c.Config.OpenTimeout,
		}, noop)
		limiters[c.ID] = ratelimiter.New(c.ID, c.Config.RateLimit, noop)
		trackers[c.ID] = orchestrator.NewEMATracker(c.ID, c.Config.TimeoutHint, c.Config, noop)
	}

	orch := orchestrator.New(
		carriers, registry, breakers, limiters, trackers,
		rec, orchestrator.Config{HedgePollInterval: 5 * time.Millisecond}, log,
		nil, // no repository
	)

	mux := http.NewServeMux()
	h := handler.New(orch, rec, reg, log, nil)
	h.RegisterRoutes(mux)

	srv := httptest.NewServer(mux)
	tb.Cleanup(srv.Close)
	return srv
}

func TestE2E_Healthz(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", string(body))
	}
}

func TestE2E_PostQuotes_HappyPath(t *testing.T) {
	srv := startTestServer(t)

	payload := `{"request_id":"e2e-01","coverage_lines":["auto"],"timeout_ms":5000}`
	resp, err := http.Post(srv.URL+"/quotes", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /quotes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		RequestID string `json:"request_id"`
		Quotes    []struct {
			CarrierID    string `json:"carrier_id"`
			PremiumCents int64  `json:"premium_cents"`
			Currency     string `json:"currency"`
			LatencyMs    int64  `json:"latency_ms"`
		} `json:"quotes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result.RequestID != "e2e-01" {
		t.Fatalf("expected request_id %q, got %q", "e2e-01", result.RequestID)
	}
	if len(result.Quotes) < 1 {
		t.Fatal("expected at least 1 quote")
	}
	for _, q := range result.Quotes {
		if q.CarrierID == "" {
			t.Fatal("quote missing carrier_id")
		}
		if q.PremiumCents <= 0 {
			t.Fatalf("expected premium_cents > 0, got %d", q.PremiumCents)
		}
		if q.Currency != "USD" {
			t.Fatalf("expected currency USD, got %q", q.Currency)
		}
		if q.LatencyMs <= 0 {
			t.Fatalf("expected latency_ms > 0, got %d", q.LatencyMs)
		}
	}
}

func TestE2E_PostQuotes_InvalidRequest(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Post(srv.URL+"/quotes", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /quotes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestE2E_PostQuotes_ShortTimeout_ExcludesSlowCarrier(t *testing.T) {
	srv := startTestServer(t)

	payload := `{"request_id":"e2e-timeout","coverage_lines":["auto"],"timeout_ms":200}`
	resp, err := http.Post(srv.URL+"/quotes", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /quotes failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Quotes []struct {
			CarrierID string `json:"carrier_id"`
		} `json:"quotes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	for _, q := range result.Quotes {
		if q.CarrierID == "gamma" {
			t.Fatal("gamma (800ms) should not appear with 200ms timeout")
		}
	}
}

func TestE2E_Metrics(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "orchestrator_fan_out_duration_seconds") {
		t.Fatal("expected metrics body to contain orchestrator_fan_out_duration_seconds")
	}
}
