// Command carrier-gateway starts the multi-carrier quote aggregation HTTP server.
// It wires all layers (domain, circuitbreaker, ratelimiter, adapter, orchestrator,
// metrics, handler) and serves on the address specified by -addr (default :8080).
//
// Graceful shutdown is triggered on SIGTERM or SIGINT. The server drains
// in-flight requests for up to 30 seconds before terminating.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/handler"
	"github.com/rechedev9/carrier-gateway/internal/metrics"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
)

// exitCodeSuccess is returned when the server shuts down cleanly.
const exitCodeSuccess = 0

// exitCodeDrainTimeout is returned when graceful shutdown times out.
const exitCodeDrainTimeout = 1

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// --- Prometheus registry ---
	reg := prometheus.NewRegistry()
	rec := metrics.New(reg)

	// --- Carrier definitions ---
	carriers := buildCarriers()

	// --- Mock carrier instances ---
	alphaCarrier := adapter.NewAlpha(log)
	betaCarrier := adapter.NewBeta(log)
	gammaCarrier := adapter.NewGamma(log)

	// --- Adapter registry ---
	reg2 := adapter.NewRegistry()
	reg2.Register("alpha", adapter.RegisterMockCarrier(alphaCarrier))
	reg2.Register("beta", adapter.RegisterMockCarrier(betaCarrier))
	reg2.Register("gamma", adapter.RegisterMockCarrier(gammaCarrier))

	// --- Per-carrier infrastructure (breakers, limiters, trackers) ---
	breakers := make(map[string]*circuitbreaker.Breaker, len(carriers))
	limiters := make(map[string]*ratelimiter.Limiter, len(carriers))
	trackers := make(map[string]*orchestrator.EMATracker, len(carriers))

	for _, c := range carriers {
		breakers[c.ID] = circuitbreaker.New(c.ID, circuitbreaker.Config{
			FailureThreshold: c.Config.FailureThreshold,
			SuccessThreshold: c.Config.SuccessThreshold,
			OpenTimeout:      c.Config.OpenTimeout,
		}, rec)

		limiters[c.ID] = ratelimiter.New(c.ID, c.Config.RateLimit, rec)

		trackers[c.ID] = orchestrator.NewEMATracker(c.ID, c.Config.TimeoutHint, c.Config, rec)
	}

	// --- Orchestrator ---
	orch := orchestrator.New(
		carriers,
		reg2,
		breakers,
		limiters,
		trackers,
		rec,
		orchestrator.Config{},
		log,
	)

	// --- HTTP handler and server ---
	mux := http.NewServeMux()
	h := handler.New(orch, rec, log)
	h.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      35 * time.Second, // slightly longer than max request timeout
		IdleTimeout:       60 * time.Second,
	}

	// --- Start server ---
	serverErrCh := make(chan error, 1)
	go func() {
		log.Info("carrier-gateway starting", slog.String("addr", *addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
		close(serverErrCh)
	}()

	// --- Wait for signal or server error ---
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	select {
	case err := <-serverErrCh:
		if err != nil {
			log.Error("server failed to start", slog.String("error", err.Error()))
			os.Exit(exitCodeDrainTimeout)
		}
	case <-sigCtx.Done():
		log.Info("shutdown signal received")
	}

	// --- Graceful shutdown ---
	if err := h.Shutdown(context.Background(), srv); err != nil {
		log.Error("graceful shutdown failed", slog.String("error", err.Error()))
		os.Exit(exitCodeDrainTimeout)
	}

	os.Exit(exitCodeSuccess)
}

// buildCarriers returns the three demo carrier domain objects with their
// operational tuning parameters.
func buildCarriers() []domain.Carrier {
	return []domain.Carrier{
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
				RateLimit: domain.RateLimitConfig{
					TokensPerSecond: 100,
					Burst:           10,
				},
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
				RateLimit: domain.RateLimitConfig{
					TokensPerSecond: 50,
					Burst:           5,
				},
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
				RateLimit: domain.RateLimitConfig{
					TokensPerSecond: 20,
					Burst:           2,
				},
			},
		},
	}
}

// Compile-time assertion that *orchestrator.Orchestrator satisfies OrchestratorPort.
var _ ports.OrchestratorPort = (*orchestrator.Orchestrator)(nil)
