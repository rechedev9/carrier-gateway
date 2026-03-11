# Exploration: Multi-Carrier Quote Aggregation Engine

**Date**: 2026-03-11T17:30:00Z
**Detail Level**: standard
**Change Name**: carrier-gateway

---

## Current State

This is a **greenfield Go project**. The repository contains only SDD scaffolding
(`openspec/config.yaml`, `AGENTS.md`) and the entry-point stub
`cmd/carrier-gateway/main.go` (not yet created). There is no existing source code
to analyse — exploration is therefore domain-driven and intent-driven.

The stated goal is to impress Agentero's CTO (InsurTech, 80+ carrier integrations)
by demonstrating production-grade Go engineering across five hard problems:

| Hard Problem | Mechanism |
|---|---|
| Latency isolation | Fan-out/fan-in with per-carrier goroutines |
| Carrier reliability | Per-carrier circuit breakers (closed/open/half-open) |
| Tail-latency mitigation | Adaptive hedging driven by EMA p95 per carrier |
| Schema heterogeneity | Generic `Adapter[T, U]` normalising carrier types to domain types |
| Throughput fairness | Per-carrier token-bucket rate limiter |

---

## Relevant Files

No source files exist yet. The artifact map below tracks what WILL be created:

| File Path | Purpose | Lines est. | Complexity | Test Coverage |
|---|---|---|---|---|
| `cmd/carrier-gateway/main.go` | Entry point, wires all layers | ~80 | low | no (integration) |
| `internal/domain/quote.go` | Core domain types: Quote, QuoteRequest, QuoteResult | ~120 | low | yes |
| `internal/domain/carrier.go` | Carrier identity, capabilities, config | ~60 | low | yes |
| `internal/domain/errors.go` | Sentinel errors for all domain failure modes | ~40 | low | yes |
| `internal/ports/quote_port.go` | CarrierPort interface (outbound), OrchestratorPort (inbound) | ~50 | low | yes |
| `internal/ports/metrics_port.go` | MetricsPort interface (side-effect port) | ~30 | low | yes |
| `internal/orchestrator/orchestrator.go` | Fan-out/fan-in engine, hedging decision loop | ~350 | high | yes |
| `internal/orchestrator/hedging.go` | EMA tracker, hedge threshold computation | ~150 | medium | yes |
| `internal/circuitbreaker/breaker.go` | Per-carrier CB state machine | ~200 | medium | yes |
| `internal/ratelimiter/limiter.go` | Token-bucket per carrier | ~120 | medium | yes |
| `internal/adapter/adapter.go` | Generic `Adapter[T, U]` contract + registry | ~100 | medium | yes |
| `internal/adapter/mock_carrier.go` | Stub/mock carrier for local testing | ~80 | low | yes |
| `internal/metrics/prometheus.go` | Prometheus MetricsPort impl | ~120 | low | yes |
| `internal/handler/http.go` | HTTP handler wiring inbound port | ~100 | low | yes |

---

## Domain Concepts

### Core Domain Types

```
QuoteRequest
  ├── RequestID     string          — idempotency / correlation
  ├── InsuredProfile struct         — risk subject (e.g. driver, property)
  ├── CoverageLines []CoverageLine  — what lines of business are requested
  ├── EffectiveDate time.Time
  └── Timeout       time.Duration   — caller's max wait budget

QuoteResult
  ├── RequestID   string
  ├── CarrierID   string
  ├── Premium     Money             — normalised monetary value
  ├── Coverages   []Coverage        — what was priced
  ├── BindURL     string            — deep-link to bind
  ├── ExpiresAt   time.Time
  ├── Latency     time.Duration     — wall-clock time for this carrier
  └── IsHedged    bool              — true if this result came from a hedge request

Carrier
  ├── ID          string            — stable slug (e.g. "progressive", "travelers")
  ├── Name        string
  ├── Config      CarrierConfig
  └── Capabilities []CoverageLine  — what this carrier can price

CarrierConfig
  ├── BaseURL       string
  ├── TimeoutHint   time.Duration   — carrier's known typical latency
  ├── RateLimit     RateLimitConfig — tokens/second, burst
  └── Priority      int             — tiebreak ordering for hedge candidates
```

### Circuit Breaker (per-Carrier State Machine)

```
States: Closed → Open → HalfOpen → Closed
                           ↓
                         Open  (if probe fails)

Closed   : normal operation; failures increment counter
Open     : all requests short-circuit with ErrCircuitOpen; timer runs
HalfOpen : single probe allowed; success → Closed, failure → Open

Parameters:
  FailureThreshold  int           — consecutive failures before Open
  SuccessThreshold  int           — consecutive successes in HalfOpen before Closed
  OpenTimeout       time.Duration — how long to stay Open before probing
  HalfOpenMaxConc   int           — max concurrent probes (usually 1)
```

### Adaptive Hedging (EMA p95 Tracker)

Hedging means: if carrier A hasn't responded within its hedge threshold, fire a
second request to a fallback carrier B simultaneously. The first response wins.

```
EMA p95 Tracker (per carrier):
  α    float64                  — smoothing factor (tunable, e.g. 0.1)
  p95  float64                  — exponentially weighted moving p95 latency (ms)
  n    int                      — observation count (used during warm-up)

Hedge Threshold:
  threshold = p95 * HedgeMultiplier   (e.g. 1.2 — hedge at 120% of p95)

On each carrier response:
  if latency > 2*p95 { p95 = p95 * (1-α) + latency * α }  // fast decay on spike
  else               { p95 = p95 * (1-α) + latency * α }   // normal update

Hedge candidate selection:
  - Carrier must be in Closed state (not Open)
  - Carrier must NOT be the original carrier
  - Prefer carrier with lowest current p95
  - Apply rate limit check before firing hedge
```

### Generic Adapter[T, U]

```go
// Adapter transforms carrier-native request/response types to domain types.
type Adapter[Req, Resp any] interface {
    ToCarrierRequest(ctx context.Context, q QuoteRequest) (Req, error)
    FromCarrierResponse(ctx context.Context, r Resp, carrierID string) (QuoteResult, error)
}
```

Each carrier implementation plugs a concrete `Adapter[CarrierXReq, CarrierXResp]`.
An adapter registry maps `CarrierID → Adapter` using an opaque wrapper to erase
the type parameters at the registry boundary (via `func(QuoteRequest) (QuoteResult, error)`
closures, not reflection).

### Rate Limiter (per-Carrier Token Bucket)

```
TokenBucket (per carrier):
  capacity   float64   — max burst
  tokens     float64   — current token count
  refillRate float64   — tokens/second
  lastRefill time.Time

Acquire(ctx) blocks until a token is available or ctx is cancelled.
Tryacquire() returns immediately: ok=true if token available, ok=false otherwise.
```

For hedge requests, `TryAcquire` is used (no blocking — hedge is best-effort).

### Metrics (Prometheus)

```
Labels: carrier_id, status (success|error|circuit_open|rate_limited|hedged|timeout)

Gauges:
  carrier_circuit_breaker_state{carrier_id}   0=closed, 1=half-open, 2=open
  carrier_p95_latency_ms{carrier_id}          EMA p95

Histograms:
  carrier_quote_latency_seconds{carrier_id, status}
  orchestrator_fan_out_duration_seconds

Counters:
  carrier_requests_total{carrier_id, status}
  hedge_requests_total{carrier_id, trigger_carrier}
  circuit_breaker_transitions_total{carrier_id, from_state, to_state}
  rate_limit_rejections_total{carrier_id}
```

---

## Dependency Map

```
cmd/carrier-gateway/main.go
  -> internal/handler/http.go          (HTTP inbound adapter)
  -> internal/orchestrator/            (core engine)
  -> internal/metrics/prometheus.go    (MetricsPort impl)
  -> internal/adapter/*                (carrier adapters)
  -> internal/circuitbreaker/          (CB per carrier)
  -> internal/ratelimiter/             (RL per carrier)

internal/orchestrator/orchestrator.go
  -> internal/ports/quote_port.go      (CarrierPort outbound interface)
  -> internal/ports/metrics_port.go    (MetricsPort side-effect interface)
  -> internal/orchestrator/hedging.go  (EMA tracker)
  -> internal/domain/                  (pure domain types, no external deps)
  -> internal/circuitbreaker/          (wrapped calls through CB)
  -> internal/ratelimiter/             (token acquisition before dispatch)

internal/adapter/adapter.go
  -> internal/domain/                  (QuoteRequest, QuoteResult)
  (no infrastructure deps — adapters import external HTTP client but domain layer does not)

External deps (Go modules):
  golang.org/x/sync/errgroup           fan-out goroutine management
  github.com/sony/gobreaker OR         circuit breaker (evaluate vs custom)
  github.com/prometheus/client_golang  Prometheus metrics
  net/http (stdlib)                    inbound handler, outbound carrier calls
  log/slog (stdlib, Go 1.21+)         structured logging
  sync/atomic                          lock-free CB state
```

**Hexagonal boundary enforcement:**
- `internal/domain/` — ZERO external imports (only stdlib: time, errors, fmt)
- `internal/ports/` — ZERO external imports (only domain types)
- `internal/orchestrator/` — imports ports and domain only (no adapter/infra)
- `internal/adapter/` — imports domain + external HTTP client
- `internal/handler/` — imports ports + domain (no adapter internals)

---

## Data Flow

### Primary Path (No Hedging, No CB Events)

```
1. HTTP POST /quotes
   └─ handler.QuoteHandler validates request → builds domain.QuoteRequest

2. Orchestrator.GetQuotes(ctx, req)
   ├─ Iterate registered carriers → select eligible (CB=Closed, RL available)
   ├─ Launch one goroutine per carrier via errgroup
   │    └─ Each goroutine:
   │         a. RateLimiter.Acquire(ctx)      ← blocks up to ctx deadline
   │         b. CircuitBreaker.Execute(ctx, fn)
   │         c. Adapter.ToCarrierRequest()
   │         d. HTTP call to carrier API
   │         e. Adapter.FromCarrierResponse()
   │         f. Record latency → EMA tracker
   │         g. Send QuoteResult to results channel
   └─ Collector goroutine reads results channel until N responses or timeout

3. Orchestrator returns []QuoteResult sorted by premium (ascending)

4. Handler serialises → 200 OK JSON response
```

### Hedge Path (Slow Carrier Triggers Hedge)

```
1-2a. Same as above — goroutines launched per carrier

2b. Hedge monitor goroutine runs alongside fan-out:
    - Ticks every HedgePollInterval (e.g. 5ms)
    - For each pending carrier: if elapsed > hedge_threshold(carrier)
         → select best-available hedge candidate
         → RateLimiter.TryAcquire(hedgeCarrier)  ← non-blocking
         → if ok: launch hedge goroutine (marks result.IsHedged=true)

2c. Results channel is shared — primary and hedge goroutines both send to it
    - First result for a given CoverageBundle wins; duplicates discarded

3-4. Same as primary path
```

### Circuit Breaker State Transitions (inline with fan-out)

```
Closed → consecutive failures ≥ FailureThreshold → Open
Open   → OpenTimeout elapsed → HalfOpen
HalfOpen → Execute() called:
           probe succeeds → Closed (reset counters)
           probe fails    → Open (restart timer)
```

### Rate Limit Path

```
Acquire(ctx):
  tokens >= 1 → decrement, return immediately
  tokens < 1  → compute wait duration until next refill
             → select { case <-time.After(wait): retry / case <-ctx.Done(): return ErrRateLimited }
```

---

## Risk Assessment

| Dimension | Level | Notes |
|---|---|---|
| Blast radius | low | Greenfield — no existing code to break |
| Type safety | medium | Generic `Adapter[T,U]` type erasure at registry boundary requires careful design; risk of runtime panics if closure capture is wrong |
| Test coverage | high risk if not planned | Fan-out, hedging, CB, RL all have non-trivial concurrent behaviour — table-driven tests with race detector (`-race`) are mandatory |
| Coupling | low | Hexagonal ports isolate domain from infra by design |
| Complexity | high | Five interacting concurrency systems (fan-out, hedging monitor, CB state machine, RL, metrics) all operating within a single request lifetime; race conditions are the primary implementation risk |
| Data integrity | low | No persistent storage — all state is in-memory per-process |
| Breaking changes | low | No existing consumers (greenfield) |
| Security surface | medium | Carrier API credentials must not leak into logs; input validation on QuoteRequest; potential for SSRF if carrier URLs are caller-supplied |

---

## Approach Comparison

### Circuit Breaker: Custom vs Library

| Approach | Pros | Cons | Effort | Risk |
|---|---|---|---|---|
| **A: Custom CB** (sync/atomic state machine) | Full control over metrics integration, CB state exposed as Prometheus gauge directly, no dependency bloat | More code to test and maintain | Medium | Medium |
| **B: gobreaker (sony/gobreaker)** | Battle-tested, standard 3-state machine, minimal code | Prometheus integration requires wrapping, less control over HalfOpen probe count | Low | Low |

**Recommendation: Option A (custom)** — the CB state must be directly observable as a Prometheus gauge (`carrier_circuit_breaker_state`) without adapter wrapping. A custom implementation is ~150 lines and gives precise control. This is also a CTO-impression project — a custom, well-tested CB demonstrates mastery.

### EMA Tracking: Global vs Per-Carrier

| Approach | Pros | Cons | Effort | Risk |
|---|---|---|---|---|
| **A: Per-carrier EMA** | Accurate — different carriers have vastly different latency profiles | More state to manage | Low | Low |
| **B: Global EMA** | Simpler | Inaccurate — fast carriers subsidise slow ones, hedge fires too late | Low | High |

**Recommendation: Option A (per-carrier EMA)** — already assumed in the topic; global EMA would be a correctness regression.

### Hedge Candidate Selection Strategy

| Approach | Pros | Cons | Effort | Risk |
|---|---|---|---|---|
| **A: Lowest p95 carrier** | Statistically best choice, uses existing EMA data | Requires sorted iteration at hedge time | Low | Low |
| **B: Random eligible carrier** | Simpler | Suboptimal under load | Low | Medium |
| **C: Priority field on Carrier** | Business-driven (can prefer preferred carriers) | Doesn't adapt to runtime conditions | Low | Low |

**Recommendation: Option A with Priority as tiebreak** — select the hedge carrier with lowest p95, break ties by `Carrier.Priority`.

### Rate Limiter: Custom vs golang.org/x/time/rate

| Approach | Pros | Cons | Effort | Risk |
|---|---|---|---|---|
| **A: Custom token bucket** | Demonstrates implementation skill, tight metrics integration | More code | Medium | Low |
| **B: golang.org/x/time/rate** | Production-proven, already in stdlib ecosystem | Less visible in code review for impression points | Low | Low |

**Recommendation: Option B (`golang.org/x/time/rate`)** — it is part of the official `golang.org/x` family, production-grade, and the rate-limiter is not the primary impression point. Save implementation complexity budget for the CB, hedging, and orchestrator.

---

## Recommendation

Proceed with a hexagonal Go project structured as follows:

```
carrier-gateway/
├── cmd/carrier-gateway/main.go
└── internal/
    ├── domain/          ← pure types, no external deps
    ├── ports/           ← interface definitions
    ├── orchestrator/    ← fan-out engine + hedging
    ├── circuitbreaker/  ← custom 3-state machine
    ├── ratelimiter/     ← thin wrapper over x/time/rate
    ├── adapter/         ← generic Adapter[T,U] + mock carrier
    ├── metrics/         ← Prometheus MetricsPort impl
    └── handler/         ← HTTP handler (inbound adapter)
```

The implementation order follows **hexagonal layers outward**:
1. `domain/` types and sentinel errors
2. `ports/` interfaces
3. `circuitbreaker/` and `ratelimiter/` (infrastructure components, no domain deps)
4. `orchestrator/` core engine (uses ports)
5. `adapter/` mock carrier + generic adapter contract
6. `metrics/` Prometheus impl
7. `handler/` HTTP wiring
8. `cmd/` main.go composition root

**Primary impression levers for the CTO:**
- EMA-based adaptive hedging (novel, operationally motivated)
- Generic `Adapter[T,U]` with type-erased registry (Go generics sophistication)
- Custom circuit breaker with Prometheus gauge emission on every state transition
- Race-condition-free design verified with `go test -race ./...`
- Full structured slog logging with `carrier_id` and `request_id` on every log line

---

## Clarification Required (BLOCKING)

### Q1: HTTP server or library only?

**Question**: Should the project expose an HTTP server (with a `/quotes` endpoint and
`/metrics` Prometheus scrape endpoint) or be structured as an importable Go library
(package) with the orchestrator as the public API?

**Why it matters**: HTTP server requires `net/http` handler wiring, graceful shutdown,
and port configuration. A library has no `main.go` logic beyond example usage — the
public API is `Orchestrator.GetQuotes(ctx, req)`. This affects the entry point design,
the `cmd/` directory, and the handler layer scope.

**Options**:
- A: HTTP server binary (endpoint-first, runnable demo, `/quotes` POST + `/metrics` GET)
  → More impressive as a deployable artifact; CTO can run it
- B: Go library package (import-first, no HTTP handler)
  → Cleaner as a component, but requires a caller to demo

**Recommendation**: Option A (HTTP server) — a runnable binary that the CTO can
`curl` directly is far more impressive than a library. The HTTP layer is thin (<100
lines) and does not compromise the hexagonal design.

### Q2: How many mock carriers should the demo include?

**Question**: The demo needs at least one carrier adapter to prove the pattern. Should
we implement 2–3 mock carriers with simulated latency profiles (to trigger hedging),
or a single mock carrier?

**Why it matters**: The hedging, circuit breaker, and fan-out mechanics only reveal
themselves when multiple carriers with different latency/failure behaviours are present.
A single mock carrier cannot demonstrate any of the five hard problems.

**Options**:
- A: 3 mock carriers with distinct latency profiles (fast/medium/slow) + configurable
  failure injection → fully demonstrates all five systems
- B: 1 mock carrier → insufficient for a CTO demo

**Recommendation**: Option A (3 mock carriers) — "CarrierAlpha" (fast, ~50ms),
"CarrierBeta" (medium, ~200ms, occasional failures), "CarrierGamma" (slow, ~800ms,
triggers hedging). This makes every system observable.

### Q3: Real carrier HTTP calls or simulation only?

**Question**: Should the adapters make real outbound HTTP calls to carrier sandbox APIs,
or should mock carriers simulate latency/failures in-process with `time.Sleep` + random
error injection?

**Why it matters**: Real HTTP calls require carrier API credentials (secrets), sandbox
environments, and network access — this creates setup friction and security risk. In-process
simulation is hermetically testable and demonstrable without credentials.

**Options**:
- A: In-process simulation (sleep + error injection, configurable per carrier)
  → Self-contained, zero credentials, fully testable, immediately runnable
- B: Real HTTP calls to external carrier sandbox APIs
  → Authentic integration, but requires secrets, sandbox access, setup steps

**Recommendation**: Option A (in-process simulation) — the architectural pattern is
identical whether the underlying HTTP call is real or simulated. A hermetic demo is
more reliable for a CTO presentation. The `Adapter[T,U]` interface makes swapping in
real implementations trivial.

---

## Open Questions (DEFERRED)

- **Graceful shutdown budget**: How long should the server wait for in-flight quote
  requests to complete before force-killing goroutines? (SIGTERM → drain window → exit)
  Can be decided during design phase. Recommended: 30 seconds.

- **EMA warm-up**: During the first N observations (e.g. N<10), the EMA is noisy.
  Should hedging be suppressed during warm-up, or use a conservative initial p95
  (e.g. 2× the carrier's `TimeoutHint`)? Decide during spec phase.

- **CB `HalfOpenMaxConc`**: Should multiple probes be allowed in HalfOpen state
  or strictly 1? Standard practice is 1. Can be a config parameter.

- **Quota accounting**: If a hedge fires and both primary and hedge return results,
  does the primary result count against rate limit quota? (Yes — it was already
  consumed.) Should the hedge response be preferred over primary? Decide during spec.

- **Metrics cardinality**: If carrier count grows to 80+ (Agentero scale), Prometheus
  label cardinality grows linearly. Is this acceptable, or should we cap at a configurable
  `max_carrier_metrics` threshold? Deferred to design.

- **Request fan-out scope**: Does every `QuoteRequest` always fan out to ALL registered
  carriers, or should the orchestrator filter by `Carrier.Capabilities` matching
  `request.CoverageLines`? Recommended: filter by capabilities. Decide during spec.

- **Result ordering**: Should `[]QuoteResult` be sorted by `Premium` ascending (cheapest
  first) or by some weighted score (latency + premium)? Decide during spec.

- **Config hot-reload**: Should carrier configs (rate limits, CB thresholds) be reloadable
  without restart via file watch or API? Deferred — start with static config.
