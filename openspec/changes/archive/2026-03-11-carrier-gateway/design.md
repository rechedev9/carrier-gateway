# Technical Design: Carrier Gateway

**Change**: carrier-gateway
**Date**: 2026-03-11T19:00:00Z
**Status**: draft
**Depends On**: proposal.md

---

## Technical Approach

The Carrier Gateway is a greenfield Go 1.22 HTTP binary structured as a **hexagonal (ports-and-adapters) application**. The domain layer (`internal/domain/`) has zero external dependencies and contains only pure value types and sentinel errors. The ports layer (`internal/ports/`) defines inbound and outbound interfaces using only domain types — no infrastructure imports. All infrastructure (circuit breaker, rate limiter, Prometheus metrics, HTTP handler, mock carriers) lives in adapter packages that implement port interfaces. This architecture means swapping out any infrastructure component requires zero changes to the orchestrator or domain.

The orchestrator is the central engine: it fans out one goroutine per eligible carrier via `errgroup`, runs a concurrent hedge-monitor goroutine that ticks every 5 ms, drains a shared results channel, and returns sorted `[]domain.QuoteResult`. The circuit breaker wraps each carrier call using a `sync/atomic` 3-state machine (Closed/Open/HalfOpen). The rate limiter wraps `golang.org/x/time/rate.Limiter` per carrier. The EMA p95 tracker is a per-carrier struct that maintains exponentially-weighted p95 latency and computes the hedge threshold. Prometheus metrics are emitted from the orchestrator, circuit breaker, and rate limiter through the `MetricsRecorder` port interface — keeping infrastructure concerns out of the domain.

Implementation order follows **hexagonal layers outward**: domain types → port interfaces → circuit breaker and rate limiter → orchestrator core and hedging → generic adapter contract and mock carriers → Prometheus metrics → HTTP handler → composition root (`cmd/`). This order guarantees each package compiles cleanly before the next layer adds a dependency on it and prevents circular imports.

---

## Architecture Decisions

| # | Decision | Choice | Alternatives Considered | Rationale |
|---|---|---|---|---|
| 1 | Circuit breaker implementation | Custom `sync/atomic` 3-state machine | `sony/gobreaker`, `mercari/go-circuitbreaker` | Direct Prometheus gauge emission on every state transition without adapter wrapping; demonstrates mastery to CTO audience; ~200 lines, fully testable; `atomic.Int32` (Go 1.19+) avoids locks |
| 2 | Rate limiter implementation | `golang.org/x/time/rate` token bucket per carrier | Custom token bucket, `uber-go/ratelimit` | Production-proven, official extended stdlib family, token-bucket semantics match the burst profile needed; frees complexity budget for CB and hedging |
| 3 | Adapter type erasure | Closure capture `func(context.Context, domain.QuoteRequest) (domain.QuoteResult, error)` | `reflect`-based registry, empty interface dispatch, code generation | Zero runtime overhead; full type safety at generic boundary; avoids reflection fragility; registry stores erasure closures keyed by carrier ID |
| 4 | Fan-out goroutine lifecycle | `golang.org/x/sync/errgroup` with `WithContext` | `sync.WaitGroup` + manual channel, `conc` library | `errgroup` propagates the first non-nil error, handles context cancellation, and is idiomatic for structured concurrency in Go; already in the extended stdlib ecosystem |
| 5 | EMA p95 granularity | Per-carrier EMA with α=0.1 and warm-up suppression (N<10) | Global EMA, per-carrier simple moving average | Global EMA conflates fast and slow carriers causing incorrect hedging; SMA requires a ring buffer; EMA with `atomic.Pointer[emaState]` is lock-free and non-blocking for read-heavy workloads |
| 6 | Hedge candidate selection | Lowest current p95 carrier, `Carrier.Priority` as tiebreak | Random eligible carrier, static priority-only | Statistically optimal; adapts to runtime conditions; Priority field breaks ties deterministically; consistent with proposal decision |
| 7 | HalfOpen probe concurrency | Strictly 1 (`HalfOpenMaxConc = 1`, enforced via `atomic.Int32` CAS) | Configurable N probes | Standard practice; prevents probe amplification; CAS enforcement is race-condition-free; proposal specified this |
| 8 | HTTP router | `net/http` stdlib `ServeMux` with `r.PathValue` (Go 1.22) | `gorilla/mux`, `chi`, `gin` | Go 1.22 stdlib mux now handles method-scoped routes (`POST /quotes`) natively; no third-party router dependency; consistent with SKILL.md guidance |
| 9 | Logging | `log/slog` structured logger with `carrier_id` and `request_id` on every record | `zap`, `zerolog`, `logrus` | `log/slog` is stdlib (Go 1.21+), structured, and the project target is Go 1.22; no external logging dependency; complies with `no_unstructured_logging` convention |
| 10 | Result ordering | Sort `[]QuoteResult` by `Premium.Amount` ascending | Composite latency+premium score, carrier priority | Premium ascending aligns with user expectation (cheapest first); proposal open question resolution |
| 11 | Fan-out scope | Filter carriers by `Carrier.Capabilities ∩ request.CoverageLines` before fan-out | Always fan-out to all carriers | Avoids unnecessary calls to carriers that cannot price the requested coverage; proposal open question resolution |
| 12 | Hedge first-arrival wins | First result per coverage bundle wins regardless of source (primary or hedge) | Prefer hedge result, prefer primary result | Minimises latency; avoids extra coordination; proposal open question resolution |

---

## Data Flow

### POST /quotes — Primary Path (No Hedging, No CB Events)

```
HTTP Client
  │
  └─► POST /quotes  (handler/http.go)
       │  MaxBytesReader(r.Body, 1 MB)
       │  json.Decode → handler.quoteRequest
       │  validateQuoteRequest() → returns 400 on error
       │  buildDomainRequest() → domain.QuoteRequest{RequestID, CoverageLines, Timeout}
       │
       └─► orchestrator.Orchestrator.GetQuotes(ctx, req)  (orchestrator/orchestrator.go)
            │
            ├─ filterEligibleCarriers(req.CoverageLines)
            │    └─ for each registered Carrier:
            │         capabilities ∩ CoverageLines non-empty?  → eligible
            │         CircuitBreaker.State() == Closed?         → eligible
            │
            ├─ ctx, cancel := context.WithTimeout(ctx, req.Timeout)
            │   defer cancel()
            │
            ├─ results := make(chan domain.QuoteResult, len(eligible))
            │
            ├─ errgroup.WithContext(ctx) → g, gCtx
            │
            ├─ [FOR EACH eligible carrier] g.Go(func() error { ... })
            │    │  a. RateLimiter.Wait(gCtx)        ← blocks until token or ctx done
            │    │  b. CircuitBreaker.Execute(gCtx, func() error {
            │    │       c. adapter.Exec(gCtx, req)  ← erasure closure
            │    │            └─ MockCarrier.Quote()  ← time.Sleep(latency) + error injection
            │    │       d. EMATracker.Record(latency)
            │    │       e. MetricsRecorder.RecordQuote(carrierID, latency, status)
            │    │       f. results <- quoteResult
            │    │       return nil
            │    │     })
            │    │  g. on CB error: MetricsRecorder.RecordCBTransition(...)
            │    └──────────────────────────────────────────────────────
            │
            ├─ [hedge monitor] go hedgeMonitor(gCtx, pending, results, ...)
            │    └─ (see hedge path below)
            │
            ├─ [collector] collect results until len(eligible) responses or ctx.Done()
            │    └─ for each result: append to []QuoteResult, dedup by CarrierID
            │
            ├─ g.Wait()   ← waits for all goroutines
            │
            └─ slices.SortFunc(results, byPremiumAscending)
                 │
                 └─► []domain.QuoteResult
                          │
                          └─► handler writes 200 OK application/json
```

### POST /quotes — Hedge Path (Slow Carrier Triggers Hedge)

```
[hedge monitor goroutine]  (orchestrator/hedging.go)
  │
  │  ticker := time.NewTicker(HedgePollInterval=5ms)
  │  pending = map[carrierID]pendingCarrier{startTime, threshold}
  │
  └─ LOOP (every 5ms until gCtx.Done() or all resolved):
       for carrierID, p := range pending:
         elapsed := time.Since(p.startTime)
         if elapsed < p.hedgeThreshold: continue
         if alreadyHedged[carrierID]: continue
         │
         ├─ hedgeCandidate = selectHedgeCandidate(
         │      eligible carriers
         │      - exclude original carrierID
         │      - exclude Open circuit breaker carriers
         │      - sort by EMATracker.P95() ascending
         │      - tiebreak by Carrier.Priority ascending
         │    )
         │
         ├─ ok := RateLimiter[hedgeCandidate].TryAcquire()  ← non-blocking
         │
         └─ if ok:
              alreadyHedged[carrierID] = true
              MetricsRecorder.RecordHedge(hedgeCarrierID, triggerCarrierID)
              go func() {
                result, err := adapter[hedgeCandidate].Exec(gCtx, req)
                if err == nil:
                  result.IsHedged = true
                  results <- result  ← shared channel, first arrival wins
              }()

[collector] deduplication rule:
  seen := map[carrierID]bool
  on receive result:
    if seen[result.CarrierID]: discard (duplicate hedge result)
    else: seen[result.CarrierID] = true; append to output
```

### Circuit Breaker State Transitions

```
Initial state: Closed (failures=0, successes=0)

[Closed]  ──── Execute() called ────────────────────────────────────────────────
  │                                                                              │
  │  fn() succeeds:                             fn() fails:                     │
  │  failures = 0 (reset)                       failures++                      │
  │  return result                              if failures >= FailureThreshold: │
  │                                               atomicState.CAS(Closed→Open)  │
  │                                               openedAt = now                │
  │                                               MetricsRecorder.RecordCBTrans  │
  │                                               return ErrCircuitOpen          │
  └────────────────────────────────────────────────────────────────────────────┘

[Open] ──── Execute() called ────────────────────────────────────────────────────
  │  if time.Since(openedAt) < OpenTimeout:
  │    return ErrCircuitOpen   ← short-circuit, no fn() call
  │  else:
  │    atomicState.CAS(Open→HalfOpen)
  │    MetricsRecorder.RecordCBTrans(Open→HalfOpen)
  │    fall through to HalfOpen handling
  └────────────────────────────────────────────────────────────────────────────

[HalfOpen] ──── Execute() called ─────────────────────────────────────────────
  │  if halfOpenInFlight.CAS(0→1) fails:
  │    return ErrCircuitOpen   ← enforce HalfOpenMaxConc=1
  │  defer halfOpenInFlight.Store(0)
  │  fn() succeeds:                             fn() fails:
  │    successes++                               atomicState.CAS(HalfOpen→Open)
  │    if successes >= SuccessThreshold:         openedAt = now
  │      atomicState.CAS(HalfOpen→Closed)       MetricsRecorder.RecordCBTrans
  │      failures = 0; successes = 0            return err
  │      MetricsRecorder.RecordCBTrans
  │      return result
  └────────────────────────────────────────────────────────────────────────────
```

### Rate Limiter Path

```
Primary goroutine:
  RateLimiter.Wait(ctx)
    └─ rate.Limiter.Wait(ctx)   ← golang.org/x/time/rate
         tokens available?  → consume 1 token, return immediately
         tokens depleted?   → wait until next refill window or ctx.Done()
         ctx.Done():        → return ErrRateLimited
                               MetricsRecorder.RecordRateLimitRejection(carrierID)

Hedge goroutine (non-blocking):
  ok := RateLimiter.TryAcquire()
    └─ rate.Limiter.Allow()
         tokens available?  → consume 1 token, return true
         tokens depleted?   → return false (hedge suppressed, no metrics increment)
```

### GET /metrics

```
HTTP Client
  └─► GET /metrics  (handler/http.go)
       └─► promhttp.Handler()
            └─► Prometheus default registry text exposition
                 Includes: carrier_circuit_breaker_state, carrier_p95_latency_ms,
                           carrier_quote_latency_seconds, orchestrator_fan_out_duration_seconds,
                           carrier_requests_total, hedge_requests_total,
                           circuit_breaker_transitions_total, rate_limit_rejections_total
```

---

## File Changes

| # | File Path (absolute) | Action | Description |
|---|---|---|---|
| 1 | `C:/Users/Reche/Desktop/gopro/go.mod` | create | Module manifest: `module github.com/rechedev9/carrier-gateway`, `go 1.22`; declares `golang.org/x/sync`, `golang.org/x/time`, `github.com/prometheus/client_golang` |
| 2 | `C:/Users/Reche/Desktop/gopro/go.sum` | create | Dependency checksums (generated by `go mod tidy`) |
| 3 | `C:/Users/Reche/Desktop/gopro/internal/domain/quote.go` | create | `QuoteRequest`, `QuoteResult`, `Money`, `Coverage`, `CoverageLine` types |
| 4 | `C:/Users/Reche/Desktop/gopro/internal/domain/carrier.go` | create | `Carrier`, `CarrierConfig`, `RateLimitConfig` types |
| 5 | `C:/Users/Reche/Desktop/gopro/internal/domain/errors.go` | create | Sentinel errors: `ErrCircuitOpen`, `ErrRateLimited`, `ErrNoEligibleCarriers`, `ErrRequestTimeout`, `ErrCarrierUnavailable` |
| 6 | `C:/Users/Reche/Desktop/gopro/internal/ports/quote_port.go` | create | `CarrierPort` (outbound), `OrchestratorPort` (inbound) interfaces; uses only domain types |
| 7 | `C:/Users/Reche/Desktop/gopro/internal/ports/metrics_port.go` | create | `MetricsRecorder` interface: `RecordQuote`, `RecordHedge`, `RecordCBTransition`, `RecordRateLimitRejection`, `RecordFanOutDuration` |
| 8 | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker.go` | create | `Breaker` struct with `sync/atomic` 3-state machine; `Execute(ctx, fn)` method; Prometheus gauge emission via `MetricsRecorder` port |
| 9 | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | create | Table-driven tests for all state transitions and concurrent probe enforcement with `-race` |
| 10 | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter.go` | create | `Limiter` struct wrapping `rate.Limiter`; `Wait(ctx)`, `TryAcquire()` methods; `NewLimiter(cfg RateLimitConfig)` constructor |
| 11 | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter_test.go` | create | Table-driven tests for token exhaustion, context cancellation, TryAcquire non-blocking semantics |
| 12 | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator.go` | create | `Orchestrator` struct; `GetQuotes(ctx, req)` method; fan-out/fan-in engine; errgroup coordination; result deduplication and sorting |
| 13 | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | create | Table-driven integration tests: all carriers respond, partial timeout, CB open short-circuit, rate limit rejection, result sorting |
| 14 | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging.go` | create | `EMATracker` struct; `Record(latency)`, `P95()`, `HedgeThreshold()` methods; `hedgeMonitor` function; warm-up suppression logic |
| 15 | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging_test.go` | create | EMA convergence tests, warm-up suppression, hedge candidate selection, concurrent record safety with `-race` |
| 16 | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter.go` | create | `Adapter[Req, Resp any]` generic interface; `AdapterFunc` type-erased closure type; `Registry` struct mapping `carrierID → AdapterFunc`; `Register[Req, Resp]` generic constructor |
| 17 | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter_test.go` | create | Type erasure correctness tests: registry with multiple concrete types, round-trip conversion fidelity |
| 18 | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier.go` | create | `MockCarrier` struct with configurable `BaseLatency`, `JitterMs`, `FailureRate`; Alpha/Beta/Gamma constructor functions |
| 19 | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier_test.go` | create | Latency profile validation, failure injection rate test, concurrent call safety |
| 20 | `C:/Users/Reche/Desktop/gopro/internal/metrics/prometheus.go` | create | `PrometheusRecorder` implementing `MetricsRecorder`; registers all gauges, histograms, and counters; `NewPrometheusRecorder(reg prometheus.Registerer)` constructor |
| 21 | `C:/Users/Reche/Desktop/gopro/internal/metrics/prometheus_test.go` | create | Metric registration, label cardinality, counter/gauge update correctness |
| 22 | `C:/Users/Reche/Desktop/gopro/internal/handler/http.go` | create | `Handler` struct; `POST /quotes` and `GET /metrics` route registration; request validation; JSON encode/decode; `MaxBytesReader`; graceful shutdown hook |
| 23 | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | create | HTTP handler unit tests: valid request, malformed JSON, oversized body, 200/400/500 responses |
| 24 | `C:/Users/Reche/Desktop/gopro/cmd/carrier-gateway/main.go` | create | Composition root: wires all layers, creates `http.Server`, installs signal handler (`SIGTERM`/`SIGINT`), `server.Shutdown(ctx)` with 30-second drain window |

**Summary**: 24 files created, 0 files modified, 0 files deleted

---

## Interfaces and Contracts

### Domain Types (`internal/domain/`)

```go
// internal/domain/quote.go

// CoverageLine is a typed string identifying a line of business.
type CoverageLine string

const (
    CoverageLineAuto       CoverageLine = "auto"
    CoverageLineHomeowners CoverageLine = "homeowners"
    CoverageLineUmbrella   CoverageLine = "umbrella"
)

// Money represents a monetary amount in a specific currency.
type Money struct {
    Amount   int64  // cents, to avoid float precision issues
    Currency string // ISO 4217, e.g. "USD"
}

// Coverage describes a single priced coverage item within a quote.
type Coverage struct {
    Line   CoverageLine
    Label  string
    Limit  Money
    Deductible Money
}

// QuoteRequest is the inbound domain type for requesting carrier quotes.
type QuoteRequest struct {
    RequestID     string
    CoverageLines []CoverageLine
    Timeout       time.Duration
}

// QuoteResult is the normalised output from a single carrier quote.
type QuoteResult struct {
    RequestID  string
    CarrierID  string
    Premium    Money
    Coverages  []Coverage
    ExpiresAt  time.Time
    Latency    time.Duration
    IsHedged   bool
}

// internal/domain/carrier.go

// RateLimitConfig specifies the token-bucket parameters for a carrier.
type RateLimitConfig struct {
    TokensPerSecond float64
    Burst           int
}

// CarrierConfig holds tuning parameters for a single carrier.
type CarrierConfig struct {
    TimeoutHint          time.Duration   // expected typical response latency
    OpenTimeout          time.Duration   // how long CB stays Open before probing
    FailureThreshold     int             // consecutive failures before Open
    SuccessThreshold     int             // consecutive successes in HalfOpen before Closed
    HedgeMultiplier      float64         // hedge fires at p95 * HedgeMultiplier
    EMAAlpha             float64         // EMA smoothing factor (0 < α < 1)
    EMAWarmupObservations int            // suppress hedging until n observations
    RateLimit            RateLimitConfig
    Priority             int             // tiebreak for hedge candidate selection (lower = preferred)
}

// Carrier is the domain identity of a carrier integration.
type Carrier struct {
    ID           string
    Name         string
    Capabilities []CoverageLine
    Config       CarrierConfig
}

// internal/domain/errors.go

var (
    ErrCircuitOpen        = errors.New("circuit open")
    ErrRateLimited        = errors.New("rate limited")
    ErrNoEligibleCarriers = errors.New("no eligible carriers")
    ErrRequestTimeout     = errors.New("request timeout")
    ErrCarrierUnavailable = errors.New("carrier unavailable")
)
```

### Port Interfaces (`internal/ports/`)

```go
// internal/ports/quote_port.go

// CarrierPort is the outbound port used by the orchestrator to request a quote
// from a single carrier. Implementations live in internal/adapter/.
type CarrierPort interface {
    // Quote requests a quote from the carrier for the given request.
    // Returns ErrCarrierUnavailable on transient carrier errors.
    Quote(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)
}

// OrchestratorPort is the inbound port for the HTTP handler to trigger a
// fan-out quote request.
type OrchestratorPort interface {
    // GetQuotes fans out to all eligible carriers and returns sorted results.
    // Returns ErrNoEligibleCarriers if no carrier can service the request.
    GetQuotes(ctx context.Context, req domain.QuoteRequest) ([]domain.QuoteResult, error)
}

// internal/ports/metrics_port.go

// CBState mirrors the circuit breaker state values for metrics labelling.
type CBState int32

const (
    CBStateClosed   CBState = 0
    CBStateHalfOpen CBState = 1
    CBStateOpen     CBState = 2
)

// MetricsRecorder is the side-effect port for emitting operational metrics.
// Implementations must be safe for concurrent use.
type MetricsRecorder interface {
    RecordQuote(carrierID string, latency time.Duration, status string)
    RecordHedge(hedgeCarrierID, triggerCarrierID string)
    RecordCBTransition(carrierID string, from, to CBState)
    RecordRateLimitRejection(carrierID string)
    RecordFanOutDuration(duration time.Duration)
    SetCBState(carrierID string, state CBState)
    SetP95Latency(carrierID string, ms float64)
}
```

### Circuit Breaker (`internal/circuitbreaker/`)

```go
// Config holds the tuneable parameters for a Breaker instance.
type Config struct {
    FailureThreshold int
    SuccessThreshold int
    OpenTimeout      time.Duration
}

// Breaker is a per-carrier 3-state circuit breaker.
// All methods are safe for concurrent use.
type Breaker struct {
    carrierID       string
    cfg             Config
    state           atomic.Int32    // 0=Closed 1=HalfOpen 2=Open
    failures        atomic.Int32
    successes       atomic.Int32
    halfOpenInFlight atomic.Int32
    openedAt        atomic.Int64    // UnixNano
    metrics         ports.MetricsRecorder
}

// New returns a Breaker initialised to Closed state.
func New(carrierID string, cfg Config, m ports.MetricsRecorder) *Breaker

// Execute runs fn through the circuit breaker.
// Returns domain.ErrCircuitOpen if the breaker is Open or HalfOpen concurrency is exceeded.
func (b *Breaker) Execute(ctx context.Context, fn func() error) error

// State returns the current CBState.
func (b *Breaker) State() ports.CBState

// Compile-time interface satisfaction check
var _ interface{ Execute(context.Context, func() error) error } = (*Breaker)(nil)
```

### Rate Limiter (`internal/ratelimiter/`)

```go
// Limiter is a per-carrier token-bucket rate limiter.
type Limiter struct {
    inner     *rate.Limiter
    carrierID string
    metrics   ports.MetricsRecorder
}

// New returns a Limiter configured from cfg.
func New(carrierID string, cfg domain.RateLimitConfig, m ports.MetricsRecorder) *Limiter

// Wait blocks until a token is available or ctx is cancelled.
// Returns domain.ErrRateLimited if ctx is cancelled before a token is acquired,
// and emits a rate_limit_rejections_total increment.
func (l *Limiter) Wait(ctx context.Context) error

// TryAcquire attempts to take a token without blocking.
// Returns true if a token was acquired; false if the bucket is empty.
// Does NOT emit a rejection metric on false — hedge suppression is silent.
func (l *Limiter) TryAcquire() bool
```

### Generic Adapter (`internal/adapter/`)

```go
// Adapter[Req, Resp] transforms carrier-native types to domain types.
// Each mock carrier implements this interface with its own request/response structs.
type Adapter[Req, Resp any] interface {
    // ToCarrierRequest converts a domain QuoteRequest to a carrier-native request.
    ToCarrierRequest(ctx context.Context, q domain.QuoteRequest) (Req, error)
    // FromCarrierResponse converts a carrier-native response to a domain QuoteResult.
    FromCarrierResponse(ctx context.Context, r Resp, carrierID string) (domain.QuoteResult, error)
}

// AdapterFunc is the type-erased form of an Adapter, stored in the Registry.
// It is a closure that captures the concrete Adapter[Req, Resp] and handles
// conversion without reflection.
type AdapterFunc func(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)

// Register creates an AdapterFunc closure that captures adapter and wraps it
// with the full ToCarrierRequest → carrier call → FromCarrierResponse pipeline.
// The carrier parameter is the CarrierPort implementation (e.g. MockCarrier).
func Register[Req, Resp any](
    adapter Adapter[Req, Resp],
    carrier interface {
        Call(ctx context.Context, req Req) (Resp, error)
    },
    carrierID string,
) AdapterFunc

// Registry maps carrier IDs to type-erased AdapterFuncs.
// Safe for concurrent reads after construction (built once at startup, never mutated).
type Registry struct {
    adapters map[string]AdapterFunc
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry

// Add registers an AdapterFunc for a carrier ID.
func (r *Registry) Add(carrierID string, fn AdapterFunc)

// Get returns the AdapterFunc for a carrier ID, or (nil, false) if not found.
func (r *Registry) Get(carrierID string) (AdapterFunc, bool)
```

### Mock Carriers (`internal/adapter/mock_carrier.go`)

```go
// MockConfig holds the simulation parameters for a mock carrier.
type MockConfig struct {
    BaseLatency time.Duration // nominal response latency
    JitterMs    int           // ±random jitter in milliseconds
    FailureRate float64       // probability of returning ErrCarrierUnavailable (0.0–1.0)
}

// MockCarrier simulates a carrier with configurable latency and failure injection.
type MockCarrier struct {
    id  string
    cfg MockConfig
    log *slog.Logger
}

// NewMockCarrier returns a MockCarrier. Call this inside Register[].
func NewMockCarrier(id string, cfg MockConfig, log *slog.Logger) *MockCarrier

// Call simulates a carrier HTTP call: sleeps for BaseLatency ± jitter,
// then returns domain.ErrCarrierUnavailable with probability FailureRate.
func (m *MockCarrier) Call(ctx context.Context, req MockRequest) (MockResponse, error)

// Predefined constructor functions for the three demo carriers:
func NewAlpha(log *slog.Logger) *MockCarrier  // BaseLatency=50ms,  JitterMs=10, FailureRate=0.0
func NewBeta(log *slog.Logger)  *MockCarrier  // BaseLatency=200ms, JitterMs=20, FailureRate=0.1
func NewGamma(log *slog.Logger) *MockCarrier  // BaseLatency=800ms, JitterMs=50, FailureRate=0.0

// MockRequest and MockResponse are the carrier-native types for mock carriers.
// They are intentionally minimal — demonstrating the type-erased adapter pattern.
type MockRequest struct {
    RequestID     string
    CoverageLines []string
}

type MockResponse struct {
    CarrierID  string
    PremiumCents int64
    ExpiresInSeconds int
}
```

### Orchestrator (`internal/orchestrator/`)

```go
// Config holds orchestrator-level parameters.
type Config struct {
    HedgePollInterval time.Duration // how often the hedge monitor ticks (default: 5ms)
}

// Orchestrator implements ports.OrchestratorPort.
type Orchestrator struct {
    carriers  []domain.Carrier
    registry  *adapter.Registry
    breakers  map[string]*circuitbreaker.Breaker
    limiters  map[string]*ratelimiter.Limiter
    trackers  map[string]*EMATracker
    metrics   ports.MetricsRecorder
    cfg       Config
    log       *slog.Logger
}

// New constructs an Orchestrator with all dependencies injected.
func New(
    carriers []domain.Carrier,
    registry *adapter.Registry,
    breakers map[string]*circuitbreaker.Breaker,
    limiters map[string]*ratelimiter.Limiter,
    trackers map[string]*EMATracker,
    metrics  ports.MetricsRecorder,
    cfg     Config,
    log     *slog.Logger,
) *Orchestrator

// GetQuotes implements ports.OrchestratorPort.
func (o *Orchestrator) GetQuotes(ctx context.Context, req domain.QuoteRequest) ([]domain.QuoteResult, error)

// Compile-time check
var _ ports.OrchestratorPort = (*Orchestrator)(nil)
```

### EMA Tracker (`internal/orchestrator/hedging.go`)

```go
// EMATracker maintains a per-carrier exponentially-weighted moving p95 latency.
// Safe for concurrent use via atomic pointer swap on state.
type EMATracker struct {
    carrierID   string
    alpha       float64
    multiplier  float64 // HedgeMultiplier
    warmup      int     // EMAWarmupObservations
    state       atomic.Pointer[emaState]
    metrics     ports.MetricsRecorder
}

type emaState struct {
    p95          float64
    observations int
}

// NewEMATracker returns a tracker seeded at 2×TimeoutHint with warm-up enabled.
func NewEMATracker(carrierID string, seed time.Duration, cfg domain.CarrierConfig, m ports.MetricsRecorder) *EMATracker

// Record updates the EMA with a new latency observation and emits the p95 gauge.
func (t *EMATracker) Record(latency time.Duration)

// P95 returns the current EMA p95 latency in milliseconds.
func (t *EMATracker) P95() float64

// HedgeThreshold returns the hedge-fire threshold in milliseconds.
// Returns math.MaxFloat64 during warm-up (suppresses hedging).
func (t *EMATracker) HedgeThreshold() float64
```

### HTTP Handler (`internal/handler/`)

```go
// Handler holds all handler dependencies.
type Handler struct {
    orch    ports.OrchestratorPort
    metrics ports.MetricsRecorder
    log     *slog.Logger
}

// New returns a Handler with dependencies injected.
func New(orch ports.OrchestratorPort, m ports.MetricsRecorder, log *slog.Logger) *Handler

// RegisterRoutes registers POST /quotes and GET /metrics on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux)

// quoteRequest is the HTTP layer's inbound JSON schema (unexported).
type quoteRequest struct {
    RequestID     string   `json:"request_id"`
    CoverageLines []string `json:"coverage_lines"`
    TimeoutMs     int      `json:"timeout_ms,omitempty"` // default 5000
}

// quoteResponse is the HTTP layer's outbound JSON schema (unexported).
type quoteResponse struct {
    RequestID string        `json:"request_id"`
    Quotes    []quoteItem   `json:"quotes"`
    DurationMs int64        `json:"duration_ms"`
}

type quoteItem struct {
    CarrierID    string  `json:"carrier_id"`
    PremiumCents int64   `json:"premium_cents"`
    Currency     string  `json:"currency"`
    IsHedged     bool    `json:"is_hedged"`
    LatencyMs    int64   `json:"latency_ms"`
}
```

### Prometheus Metrics (`internal/metrics/`)

```go
// PrometheusRecorder implements ports.MetricsRecorder using Prometheus client_golang.
type PrometheusRecorder struct {
    cbStateGauge        *prometheus.GaugeVec
    p95LatencyGauge     *prometheus.GaugeVec
    quoteLatencyHist    *prometheus.HistogramVec
    fanOutDurationHist  *prometheus.HistogramVec
    requestsTotal       *prometheus.CounterVec
    hedgesTotal         *prometheus.CounterVec
    cbTransitionsTotal  *prometheus.CounterVec
    rateLimitTotal      *prometheus.CounterVec
}

// New registers all metrics with reg and returns a PrometheusRecorder.
// Panics if registration fails (called at startup).
func New(reg prometheus.Registerer) *PrometheusRecorder

// Compile-time check
var _ ports.MetricsRecorder = (*PrometheusRecorder)(nil)
```

### API Contracts

```
POST /quotes
  Content-Type: application/json
  Request Body: {
    "request_id": string (required, non-empty),
    "coverage_lines": [string] (required, ≥1 element, each must be valid CoverageLine),
    "timeout_ms": int (optional, default 5000, range 100–30000)
  }
  Response 200 OK: {
    "request_id": string,
    "quotes": [
      {
        "carrier_id": string,
        "premium_cents": int64,
        "currency": "USD",
        "is_hedged": bool,
        "latency_ms": int64
      }
    ],
    "duration_ms": int64
  }
  Response 400 Bad Request: {
    "error": "invalid request: <reason>"
  }
  Response 500 Internal Server Error: {
    "error": "internal error"
  }

GET /metrics
  Response 200 OK: Prometheus text exposition format (text/plain; version=0.0.4)
  Key metrics exposed:
    carrier_circuit_breaker_state{carrier_id}        gauge   0=closed,1=halfopen,2=open
    carrier_p95_latency_ms{carrier_id}               gauge   EMA p95 in milliseconds
    carrier_quote_latency_seconds{carrier_id,status} histogram
    orchestrator_fan_out_duration_seconds            histogram
    carrier_requests_total{carrier_id,status}        counter status: success|error|circuit_open|rate_limited|timeout
    hedge_requests_total{carrier_id,trigger_carrier} counter
    circuit_breaker_transitions_total{carrier_id,from_state,to_state} counter
    rate_limit_rejections_total{carrier_id}          counter
```

---

## Testing Strategy

| # | What to Test | Type | File Path | Maps to Requirement |
|---|---|---|---|---|
| 1 | CB: Closed → Open on FailureThreshold consecutive failures | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-001 |
| 2 | CB: Open short-circuits without calling fn | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-001 |
| 3 | CB: Open → HalfOpen after OpenTimeout | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-002 |
| 4 | CB: HalfOpen → Closed on SuccessThreshold successes | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-003 |
| 5 | CB: HalfOpen → Open on probe failure | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-004 |
| 6 | CB: HalfOpen enforces MaxConc=1 (second concurrent call returns ErrCircuitOpen) | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-005 |
| 7 | CB: Prometheus gauge emitted on every state transition | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-006 |
| 8 | CB: Concurrent Execute calls — no data race with `-race` | unit | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker_test.go` | CB-005 |
| 9 | RL: Wait acquires token when bucket has capacity | unit | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter_test.go` | RL-001 |
| 10 | RL: Wait blocks then acquires after refill | unit | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter_test.go` | RL-001 |
| 11 | RL: Wait returns ErrRateLimited when ctx cancelled | unit | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter_test.go` | RL-002 |
| 12 | RL: TryAcquire returns false without blocking when bucket empty | unit | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter_test.go` | RL-003 |
| 13 | EMA: P95 converges from seed toward observed latency over N records | unit | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging_test.go` | HEDGE-001 |
| 14 | EMA: HedgeThreshold returns MaxFloat64 during warm-up (observations < EMAWarmupObservations) | unit | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging_test.go` | HEDGE-002 |
| 15 | EMA: HedgeThreshold = P95 × HedgeMultiplier after warm-up | unit | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging_test.go` | HEDGE-001 |
| 16 | EMA: Concurrent Record calls — no data race with `-race` | unit | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging_test.go` | HEDGE-001 |
| 17 | Adapter: Registry.Get returns correct AdapterFunc for each registered carrier | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter_test.go` | ADAPTER-001 |
| 18 | Adapter: Type-erased AdapterFunc round-trips QuoteRequest → MockRequest → QuoteResult correctly | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter_test.go` | ADAPTER-001 |
| 19 | Adapter: Registry.Get on unknown carrier returns (nil, false) | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter_test.go` | ADAPTER-001 |
| 20 | MockCarrier: Alpha returns result within 50ms±jitter, no failures | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier_test.go` | MOCK-001 |
| 21 | MockCarrier: Beta returns ErrCarrierUnavailable at approximately FailureRate frequency | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier_test.go` | MOCK-002 |
| 22 | MockCarrier: Gamma returns result within 800ms±jitter | unit | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier_test.go` | MOCK-003 |
| 23 | Orchestrator: All carriers respond → returns ≥2 results sorted by premium ascending | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-001 |
| 24 | Orchestrator: Carrier with Open CB is excluded from fan-out | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-002 |
| 25 | Orchestrator: Carrier whose RateLimiter blocks returns ErrRateLimited and is excluded | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-003 |
| 26 | Orchestrator: Timeout before all carriers respond → returns partial results | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-004 |
| 27 | Orchestrator: Hedge fires when carrier exceeds HedgeThreshold, IsHedged=true on result | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-005 |
| 28 | Orchestrator: No goroutine leak after request completes (verify with goleak) | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-006 |
| 29 | Orchestrator: Fan-out with -race, 100 concurrent GetQuotes → no data races | integration | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator_test.go` | ORCH-007 |
| 30 | Handler: POST /quotes valid request → 200 with JSON body | unit | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | HTTP-001 |
| 31 | Handler: POST /quotes malformed JSON → 400 | unit | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | HTTP-002 |
| 32 | Handler: POST /quotes body > 1MB → 400 | unit | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | HTTP-003 |
| 33 | Handler: POST /quotes missing request_id → 400 | unit | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | HTTP-004 |
| 34 | Handler: GET /metrics → 200, response contains carrier_circuit_breaker_state | unit | `C:/Users/Reche/Desktop/gopro/internal/handler/http_test.go` | HTTP-005 |
| 35 | Metrics: PrometheusRecorder emits gauge on SetCBState | unit | `C:/Users/Reche/Desktop/gopro/internal/metrics/prometheus_test.go` | METRICS-001 |
| 36 | Metrics: PrometheusRecorder emits counter on RecordHedge | unit | `C:/Users/Reche/Desktop/gopro/internal/metrics/prometheus_test.go` | METRICS-002 |

### Test Dependencies

- **Mocks needed**: `ports.MetricsRecorder` — a `noopRecorder` (counting stub) used in circuit breaker, rate limiter, EMA, and orchestrator unit tests to verify metric call counts without Prometheus overhead
- **Fixtures needed**: `domain.QuoteRequest` builder function; `domain.Carrier` factory for each of Alpha/Beta/Gamma profiles; pre-warmed `EMATracker` (bypasses warm-up by pre-loading N observations)
- **Infrastructure**: No external services or test containers — all tests are in-process. Use `go test -race ./...` for concurrency tests. Use `go.uber.org/goleak` in orchestrator tests to detect goroutine leaks.

---

## Migration and Rollout

No migration or rollout steps required — this is a greenfield project with no existing production system.

### Deployment Steps (for demo use)

1. Clone repository and ensure Go 1.22+ is installed.
2. Run `go mod tidy` to download dependencies.
3. Run `go build ./cmd/carrier-gateway/` to produce the binary.
4. Start the server: `./carrier-gateway` (default port 8080).
5. Verify: `curl -s -X POST http://localhost:8080/quotes -H 'Content-Type: application/json' -d '{"request_id":"demo-1","coverage_lines":["auto"]}' | jq .`
6. Check metrics: `curl -s http://localhost:8080/metrics | grep carrier_`

### Rollback Steps

As specified in the proposal: delete `cmd/`, `internal/`, `go.mod`, and `go.sum`. The `openspec/` directory is preserved. See `proposal.md § Rollback Plan` for the git-based rollback procedure.

---

## Open Questions

The following technical questions arose during design and should be confirmed before `sdd-tasks` is run. None are blocking for task generation — defaults are documented below.

- **Goroutine leak detection library**: `go.uber.org/goleak` is the idiomatic choice for detecting goroutine leaks in Go tests. It needs to be added to `go.mod` as a `_test` import. Confirm this is acceptable or if a different approach is preferred.
- **Default server port**: Hardcoded to `8080` in `main.go`. Should this be configurable via a command-line flag (e.g., `-addr :8080`) using `flag` stdlib? Recommended: yes, add `-addr` flag.
- **Prometheus registry**: Using `prometheus.DefaultRegisterer` vs. a custom registry. Using a custom registry is preferable for testing (avoids global state pollution). Confirmed in design: `NewPrometheusRecorder(reg prometheus.Registerer)` accepts the registry as a parameter; `main.go` passes `prometheus.DefaultRegisterer`.
- **`go.uber.org/goleak` version compatibility**: Verify `goleak` supports Go 1.22 before adding to `go.mod`.

---

**Next Step**: After both design and specs are complete, run `sdd-tasks` to create the implementation checklist.
