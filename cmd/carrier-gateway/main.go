// Command carrier-gateway starts the multi-carrier quote aggregation HTTP server.
// It wires all layers (domain, circuitbreaker, ratelimiter, adapter, orchestrator,
// metrics, handler) and serves on the address specified by -addr (default :8080).
//
// Graceful shutdown is triggered on SIGTERM or SIGINT. The server drains
// in-flight requests for up to 30 seconds before terminating.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/cleanup"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/handler"
	"github.com/rechedev9/carrier-gateway/internal/metrics"
	"github.com/rechedev9/carrier-gateway/internal/middleware"
	"github.com/rechedev9/carrier-gateway/internal/orchestrator"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
	"github.com/rechedev9/carrier-gateway/internal/repository"
)

// exitCodeSuccess is returned when the server shuts down cleanly.
const exitCodeSuccess = 0

// exitCodeDrainTimeout is returned when graceful shutdown times out.
const exitCodeDrainTimeout = 1

func main() {
	defaultAddr := ":8080"
	if envAddr := os.Getenv("ADDR"); envAddr != "" {
		defaultAddr = envAddr
	}
	addr := flag.String("addr", defaultAddr, "HTTP listen address")
	flag.Parse()

	var logLevel slog.LevelVar
	logLevel.Set(slog.LevelInfo)
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		var lvl slog.Level
		if err := lvl.UnmarshalText([]byte(raw)); err == nil {
			logLevel.Set(lvl)
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: &logLevel}))

	// --- Prometheus registry ---
	reg := prometheus.NewRegistry()
	rec := metrics.New(reg)

	// --- Carrier definitions ---
	carriers := buildCarriers()

	// --- Optional PostgreSQL repository ---
	// When DATABASE_URL is set the gateway caches quotes and returns them on
	// repeated requests with the same request_id before the quote expires.
	var (
		repo ports.QuoteRepository
		db   *sql.DB
	)
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		var err error
		db, err = repository.Open(dsn)
		if err != nil {
			log.Warn("postgres unavailable, running without persistence",
				slog.String("error", err.Error()),
			)
		} else {
			pg := repository.New(db)
			if err := pg.Migrate(context.Background()); err != nil {
				log.Warn("postgres migration failed, running without persistence",
					slog.String("error", err.Error()),
				)
			} else {
				repo = pg
				log.Info("postgres repository connected")
			}
		}
	}

	// --- Expired-quote cleanup ticker ---
	const defaultCleanupInterval = 5 * time.Minute
	var cleanupTicker *cleanup.Ticker
	if repo != nil {
		interval := defaultCleanupInterval
		if raw := os.Getenv("CLEANUP_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				log.Warn("invalid CLEANUP_INTERVAL, using default",
					slog.String("value", raw),
					slog.String("error", err.Error()),
					slog.Duration("default", defaultCleanupInterval),
				)
			} else {
				interval = parsed
			}
		}
		cleanupTicker = cleanup.New(repo, interval, log)
		go cleanupTicker.Start(context.Background())
	}

	// --- Mock carrier instances ---
	alphaCarrier := adapter.NewAlpha(log)
	betaCarrier := adapter.NewBeta(log)
	gammaCarrier := adapter.NewGamma(log)

	// --- Adapter registry ---
	reg2 := adapter.NewRegistry()
	reg2.Register("alpha", adapter.RegisterMockCarrier(alphaCarrier))
	reg2.Register("beta", adapter.RegisterMockCarrier(betaCarrier))
	reg2.Register("gamma", adapter.RegisterMockCarrier(gammaCarrier))

	// --- Optional Delta (HTTP) carrier ---
	// Registered only when DELTA_BASE_URL is configured — graceful degradation.
	if baseURL := os.Getenv("DELTA_BASE_URL"); baseURL != "" {
		deltaCarrier := adapter.NewDeltaCarrier(adapter.HTTPCarrierConfig{
			BaseURL:    baseURL,
			APIKey:     os.Getenv("DELTA_API_KEY"),
			MaxRetries: 3,
			RetryDelay: 100 * time.Millisecond,
			Timeout:    2 * time.Second,
		}, log)
		reg2.Register("delta", adapter.RegisterDeltaCarrier(deltaCarrier))
		carriers = append(carriers, domain.Carrier{
			ID:           "delta",
			Name:         "Delta Insurance",
			Capabilities: []domain.CoverageLine{domain.CoverageLineAuto, domain.CoverageLineHomeowners},
			Config: domain.CarrierConfig{
				TimeoutHint:           300 * time.Millisecond,
				FailureThreshold:      5,
				SuccessThreshold:      2,
				OpenTimeout:           30 * time.Second,
				HedgeMultiplier:       1.5,
				EMAAlpha:              0.1,
				EMAWindowSize:         19,
				EMAWarmupObservations: 10,
				Priority:              4,
				RateLimit: domain.RateLimitConfig{
					TokensPerSecond: 50,
					Burst:           5,
				},
			},
		})
		log.Info("delta carrier registered", slog.String("base_url", baseURL))
	}

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
		orchestrator.Config{HedgePollInterval: 5 * time.Millisecond},
		log,
		repo, // nil when DATABASE_URL is unset
	)

	// --- API key authentication ---
	apiKeys := parseAPIKeys(os.Getenv("API_KEYS"))
	if len(apiKeys) == 0 {
		log.Error("API_KEYS env var is required (comma-separated bearer tokens)")
		os.Exit(1)
	}

	// --- HTTP handler and server ---
	mux := http.NewServeMux()
	h := handler.New(orch, rec, reg, log, db)
	h.RegisterRoutes(mux)

	// --- Concurrency limit ---
	const defaultMaxConcurrent = 100
	maxConcurrent := defaultMaxConcurrent
	if raw := os.Getenv("MAX_CONCURRENT_QUOTES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxConcurrent = n
		} else {
			log.Warn("invalid MAX_CONCURRENT_QUOTES, using default",
				slog.String("value", raw),
				slog.Int("default", defaultMaxConcurrent),
			)
		}
	}

	skipAuth := []string{"/healthz", "/metrics", "/readyz"}
	finalHandler := middleware.AuditLog(
		middleware.SecurityHeaders(
			middleware.RequireAPIKey(
				middleware.LimitConcurrency(mux, maxConcurrent, log),
				apiKeys, skipAuth, log,
			),
		), log,
	)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           finalHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
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
	if cleanupTicker != nil {
		cleanupTicker.Stop()
	}

	if err := h.Shutdown(context.Background(), srv); err != nil {
		log.Error("graceful shutdown failed", slog.String("error", err.Error()))
		os.Exit(exitCodeDrainTimeout)
	}

	// --- Close DB connection pool ---
	if db != nil {
		if err := db.Close(); err != nil {
			log.Warn("postgres connection close failed", slog.String("error", err.Error()))
		} else {
			log.Info("postgres connection closed")
		}
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

// parseAPIKeys splits a comma-separated string into non-empty keys.
func parseAPIKeys(raw string) []string {
	var keys []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// Compile-time assertion that *orchestrator.Orchestrator satisfies OrchestratorPort.
var _ ports.OrchestratorPort = (*orchestrator.Orchestrator)(nil)
