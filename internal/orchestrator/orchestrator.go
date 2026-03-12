package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/rechedev9/carrier-gateway/internal/adapter"
	"github.com/rechedev9/carrier-gateway/internal/circuitbreaker"
	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
	"github.com/rechedev9/carrier-gateway/internal/ratelimiter"
)

// defaultHedgePollInterval is used when Config.HedgePollInterval is zero.
const defaultHedgePollInterval = 5 * time.Millisecond

// defaultRequestTimeout is used when QuoteRequest.Timeout is zero.
const defaultRequestTimeout = 5 * time.Second

// Config holds orchestrator-level configuration.
type Config struct {
	// HedgePollInterval is how often the hedge monitor checks carrier latencies.
	// Defaults to 5 ms.
	HedgePollInterval time.Duration
}

// Orchestrator implements ports.OrchestratorPort. It fans out quote requests
// to all eligible carriers in parallel, runs an adaptive hedge monitor, and
// returns results sorted by premium ascending.
//
// All fields are set once at construction and treated as immutable during
// GetQuotes — making the struct safe for concurrent use.
type Orchestrator struct {
	carriers []domain.Carrier
	registry *adapter.Registry
	breakers map[string]*circuitbreaker.Breaker
	limiters map[string]*ratelimiter.Limiter
	trackers map[string]*EMATracker
	metrics  ports.MetricsRecorder
	repo     ports.QuoteRepository // optional; nil disables persistence
	cfg      Config
	log      *slog.Logger
	sfGroup  singleflight.Group // deduplicates concurrent requests with same request_id
}

// New constructs an Orchestrator with all dependencies injected.
// carriers, registry, breakers, limiters, and trackers must be fully
// populated before New is called — the Orchestrator does not modify them.
//
// repo is optional: pass nil to disable quote caching and persistence.
// When non-nil, GetQuotes checks the cache before fanning out and saves
// results after the fan-out completes.
func New(
	carriers []domain.Carrier,
	registry *adapter.Registry,
	breakers map[string]*circuitbreaker.Breaker,
	limiters map[string]*ratelimiter.Limiter,
	trackers map[string]*EMATracker,
	metrics ports.MetricsRecorder,
	cfg Config,
	log *slog.Logger,
	repo ports.QuoteRepository,
) *Orchestrator {
	if cfg.HedgePollInterval <= 0 {
		cfg.HedgePollInterval = defaultHedgePollInterval
	}
	return &Orchestrator{
		carriers: carriers,
		registry: registry,
		breakers: breakers,
		limiters: limiters,
		trackers: trackers,
		metrics:  metrics,
		repo:     repo,
		cfg:      cfg,
		log:      log,
	}
}

// GetQuotes implements ports.OrchestratorPort.
//
// It fans out to all eligible carriers in parallel using errgroup, runs a
// concurrent hedge monitor, collects and deduplicates results, then returns
// them sorted by premium ascending. Partial results are returned when the
// request context deadline arrives before all carriers respond.
//
// When a QuoteRepository is configured, GetQuotes first checks the cache.
// A cache hit (non-expired results for req.RequestID) short-circuits the
// fan-out and returns the cached results directly. On a cache miss, results
// are saved after the fan-out completes.
func (o *Orchestrator) GetQuotes(ctx context.Context, req domain.QuoteRequest) ([]domain.QuoteResult, error) {
	// Cache lookup — only when a repository is wired in.
	if o.repo != nil && req.RequestID != "" {
		if cached, ok, err := o.repo.FindByRequestID(ctx, req.RequestID); err != nil {
			o.log.Warn("cache lookup failed, proceeding with fan-out",
				slog.String("request_id", req.RequestID),
				slog.String("error", err.Error()),
			)
		} else if ok {
			o.log.Info("cache hit, returning stored quotes",
				slog.String("request_id", req.RequestID),
				slog.Int("count", len(cached)),
			)
			return cached, nil
		}
	}

	// Deduplicate concurrent requests with the same request_id.
	// singleflight ensures only one fan-out runs per request_id; other callers
	// share the result. We use context.WithoutCancel so the fan-out is not
	// tied to the first caller's deadline — fanOut creates its own timeout
	// from req.Timeout.
	if req.RequestID != "" {
		v, err, shared := o.sfGroup.Do(req.RequestID, func() (any, error) {
			return o.fanOut(context.WithoutCancel(ctx), req)
		})
		if shared {
			o.log.Debug("singleflight shared result",
				slog.String("request_id", req.RequestID),
			)
		}
		if err != nil {
			return nil, err
		}
		return v.([]domain.QuoteResult), nil
	}
	return o.fanOut(ctx, req)
}

// fanOut performs the actual carrier fan-out, hedge monitoring, result
// collection, sorting, and optional persistence.
func (o *Orchestrator) fanOut(ctx context.Context, req domain.QuoteRequest) ([]domain.QuoteResult, error) {
	start := time.Now()
	defer func() {
		o.metrics.RecordFanOutDuration(time.Since(start))
	}()

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	eligible := o.filterEligibleCarriers(req.CoverageLines)
	if len(eligible) == 0 {
		o.log.Warn("no eligible carriers after capability filter",
			slog.String("request_id", req.RequestID),
			slog.Any("requested_lines", req.CoverageLines),
		)
		return []domain.QuoteResult{}, nil
	}

	results := make(chan domain.QuoteResult, len(eligible)*2)

	hedgeable := make([]hedgeCarrier, 0, len(eligible))
	for _, c := range eligible {
		carrierID := c.ID
		breaker := o.breakers[carrierID]
		limiter := o.limiters[carrierID]
		tracker := o.trackers[carrierID]
		execFn, ok := o.registry.Get(carrierID)
		if !ok {
			o.log.Error("no adapter registered for carrier, skipping hedge",
				slog.String("carrier_id", carrierID),
			)
			continue
		}
		hedgeable = append(hedgeable, hedgeCarrier{
			carrier:    c,
			p95Ms:      tracker.P95(),
			tryAcquire: limiter.TryAcquire,
			cbState:    breaker.State,
			exec:       adapterExecFn(execFn),
		})
	}

	pending := make(map[string]pendingCarrier, len(eligible))
	for _, c := range eligible {
		pending[c.ID] = pendingCarrier{
			startTime:      time.Now(),
			hedgeThreshold: o.trackers[c.ID].HedgeThreshold(),
		}
	}

	g, gCtx := errgroup.WithContext(reqCtx)

	// hedgeMonitor runs outside the errgroup so it exits as soon as the primary
	// carriers finish — not when reqCtx times out. hedgeCtx is cancelled by the
	// collector goroutine once g.Wait() returns.
	hedgeCtx, hedgeCancel := context.WithCancel(gCtx)
	hedgeDone := make(chan struct{})
	go func() {
		hedgeMonitor(hedgeCtx, pending, results, hedgeable, req, o.metrics, o.log, o.cfg.HedgePollInterval)
		close(hedgeDone)
	}()

	for _, c := range eligible {
		carrier := c
		g.Go(func() error {
			return o.callCarrier(gCtx, carrier, req, results)
		})
	}

	collected := make([]domain.QuoteResult, 0, len(eligible))
	seen := make(map[string]bool, len(eligible))

	go func() {
		_ = g.Wait()  // wait for primary carriers only
		hedgeCancel() // signal hedgeMonitor to stop
		<-hedgeDone   // wait for hedgeMonitor + its hedge goroutines
		close(results)
	}()

	for result := range results {
		if seen[result.CarrierID] {
			o.log.Debug("duplicate carrier result discarded (hedge dedup)",
				slog.String("request_id", req.RequestID),
				slog.String("carrier_id", result.CarrierID),
			)
			continue
		}
		seen[result.CarrierID] = true
		collected = append(collected, result)
	}

	slices.SortFunc(collected, func(a, b domain.QuoteResult) int {
		if n := cmp.Compare(a.Premium.Amount, b.Premium.Amount); n != 0 {
			return n
		}
		return cmp.Compare(a.CarrierID, b.CarrierID)
	})

	o.log.Info("fan-out complete",
		slog.String("request_id", req.RequestID),
		slog.Int("eligible_carriers", len(eligible)),
		slog.Int("results_returned", len(collected)),
	)

	if o.repo != nil && req.RequestID != "" && len(collected) > 0 {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer saveCancel()
		if err := o.repo.Save(saveCtx, req.RequestID, collected); err != nil {
			o.log.Warn("failed to save quotes to repository",
				slog.String("request_id", req.RequestID),
				slog.String("error", err.Error()),
			)
		}
	}

	return collected, nil
}

// filterEligibleCarriers returns carriers whose Capabilities intersect with
// the requested coverage lines and whose circuit breakers are not Open.
// HalfOpen carriers are included — they receive a probe call per REQ-CB-003.
func (o *Orchestrator) filterEligibleCarriers(lines []domain.CoverageLine) []domain.Carrier {
	requested := make(map[domain.CoverageLine]bool, len(lines))
	for _, l := range lines {
		requested[l] = true
	}

	eligible := make([]domain.Carrier, 0, len(o.carriers))
	for _, c := range o.carriers {
		// Skip carriers with Open circuit breakers to avoid launching
		// goroutines that will immediately return ErrCircuitOpen.
		if breaker, ok := o.breakers[c.ID]; ok && breaker.State() == ports.CBStateOpen {
			continue
		}
		for _, cap := range c.Capabilities {
			if requested[cap] {
				eligible = append(eligible, c)
				break
			}
		}
	}
	return eligible
}

// callCarrier executes the full rate-limit → circuit-breaker → adapter pipeline
// for a single carrier. It sends the result to the results channel and records
// metrics. Errors are handled internally — they are logged and recorded but not
// propagated via the errgroup (partial results are acceptable).
func (o *Orchestrator) callCarrier(
	ctx context.Context,
	carrier domain.Carrier,
	req domain.QuoteRequest,
	results chan<- domain.QuoteResult,
) error {
	limiter := o.limiters[carrier.ID]
	breaker := o.breakers[carrier.ID]
	tracker := o.trackers[carrier.ID]
	execFn, ok := o.registry.Get(carrier.ID)
	if !ok {
		o.log.Error("no adapter registered for carrier",
			slog.String("request_id", req.RequestID),
			slog.String("carrier_id", carrier.ID),
		)
		return nil
	}

	// Step 1: rate limiter — blocks until token or ctx done.
	if err := limiter.Wait(ctx); err != nil {
		o.log.Info("carrier skipped: rate limited",
			slog.String("request_id", req.RequestID),
			slog.String("carrier_id", carrier.ID),
		)
		o.metrics.RecordQuote(carrier.ID, 0, "rate_limited")
		return nil // partial result — do not fail the entire fan-out
	}

	// Step 2: circuit breaker wraps the adapter call.
	callStart := time.Now()
	cbErr := breaker.Execute(ctx, func() error {
		result, err := execFn(ctx, req)
		if err != nil {
			return err
		}
		latency := time.Since(callStart)
		result.RequestID = req.RequestID
		result.Latency = latency

		tracker.Record(latency)
		o.metrics.RecordQuote(carrier.ID, latency, "success")

		o.log.Info("carrier responded",
			slog.String("request_id", req.RequestID),
			slog.String("carrier_id", carrier.ID),
			slog.Duration("latency", latency),
		)

		select {
		case results <- result:
		case <-ctx.Done():
		}
		return nil
	})

	if cbErr != nil {
		latency := time.Since(callStart)
		status := classifyError(cbErr)
		o.metrics.RecordQuote(carrier.ID, latency, status)
		o.log.Info("carrier call failed",
			slog.String("request_id", req.RequestID),
			slog.String("carrier_id", carrier.ID),
			slog.String("status", status),
			slog.String("error", cbErr.Error()),
		)
	}

	return nil // always return nil — partial results are acceptable
}

// classifyError maps a carrier call error to a status string for metrics.
func classifyError(err error) string {
	switch {
	case errors.Is(err, domain.ErrCircuitOpen):
		return "circuit_open"
	case errors.Is(err, domain.ErrRateLimitExceeded):
		return "rate_limited"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, domain.ErrCarrierTimeout):
		return "timeout"
	default:
		return "error"
	}
}

// Compile-time assertion that Orchestrator satisfies ports.OrchestratorPort.
var _ ports.OrchestratorPort = (*Orchestrator)(nil)
