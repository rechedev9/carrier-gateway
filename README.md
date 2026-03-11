# Carrier Gateway

A multi-carrier quote aggregation engine that fans out requests to N insurance carriers in parallel, hedges slow carriers adaptively, and returns sorted results -- all behind a single HTTP endpoint. Built in Go 1.22 with zero web frameworks, zero ORMs, and a hexagonal architecture where the domain layer has exactly zero external imports.

## Architecture

```
                       +-----------------+
                       |  HTTP Handler   |  POST /quotes, GET /metrics
                       | (net/http 1.22) |
                       +--------+--------+
                                |
                   +------------v-----------+
                   |     Orchestrator       |
                   | fan-out / fan-in       |
                   | hedge monitor (5ms)    |
                   | dedup + sort           |
                   +--+------+------+------++
                      |      |      |      |
               +------+  +---+--+  ++-+  +-+-------+
               | Rate |  | Circ |  |EMA|  | Adapter |
               | Limit|  | Brkr |  |p95|  | Registry|
               +------+  +------+  +---+  +-+--+--++
                                           |   |   |
            .------------------------------'   |   '----.
            |               .------------------'         |
      +-----v----+   +-----v----+   +------v-----+
      |  Alpha   |   |   Beta   |   |   Gamma    |
      | 50ms/0%  |   | 200ms/10%|   |  800ms/0%  |
      +----------+   +----------+   +------------+
```

**Layer rules:**
- `internal/domain` -- pure value types, sentinel errors. Imports only `errors` and `time`.
- `internal/ports` -- interfaces (`CarrierPort`, `OrchestratorPort`, `MetricsRecorder`). Imports only `domain` + stdlib.
- Everything else (`circuitbreaker`, `ratelimiter`, `orchestrator`, `adapter`, `metrics`, `handler`) -- implements ports. No package imports the domain backward.

## The 5 Hard Problems (and How They're Solved)

### 1. Custom Circuit Breaker with Lock-Free 3-State Machine

Three states (Closed/HalfOpen/Open) managed entirely via `sync/atomic.Int32` and CAS operations. No mutexes. HalfOpen permits exactly one probe goroutine via atomic compare-and-swap on an in-flight counter. Every state transition emits a Prometheus gauge update inline -- not deferred, not buffered.

**File:** `internal/circuitbreaker/breaker.go` (186 lines)

### 2. Adaptive Hedging with EMA p95 Per Carrier

Each carrier has an `EMATracker` that maintains an exponentially-weighted p95 latency estimate using `atomic.Pointer` for lock-free reads. The hedge monitor goroutine ticks every 5ms, checks each pending carrier against its `P95 * multiplier` threshold, and fires a hedge request to the fastest available alternative. During warm-up (< N observations), hedging is suppressed via `math.MaxFloat64` threshold.

**File:** `internal/orchestrator/hedging.go` (238 lines)

### 3. Generic Adapter with Type-Erased Registry

`Adapter[Req, Resp]` is a generic interface with `ToCarrierRequest` and `FromCarrierResponse` methods. The `Register` function captures concrete types into an `AdapterFunc` closure -- a `func(context.Context, QuoteRequest) (QuoteResult, error)`. The `Registry` stores these closures by carrier ID. Zero reflection. Full type safety at the generic boundary, full erasure at the registry boundary.

**File:** `internal/adapter/adapter.go` (91 lines)

### 4. Fan-Out/Fan-In with Structured Concurrency

The orchestrator filters eligible carriers by coverage capability, spawns one goroutine per carrier via `errgroup.WithContext`, and collects results on a buffered channel. A parallel hedge monitor goroutine watches for slow carriers. Results are deduplicated by carrier ID (first arrival wins, whether primary or hedge) and sorted by premium ascending.

**File:** `internal/orchestrator/orchestrator.go` (300 lines)

### 5. Per-Carrier Rate Limiting with Token Bucket

Each carrier gets its own `rate.Limiter` (from `golang.org/x/time/rate`) configured with independent tokens-per-second and burst capacity. The limiter exposes both blocking (`Wait`) and non-blocking (`TryAcquire`) modes -- blocking for primary requests, non-blocking for hedge requests. Rejections emit Prometheus counters.

**File:** `internal/ratelimiter/limiter.go` (62 lines)

## How to Run

```bash
# Build
go build ./cmd/carrier-gateway

# Start the server (defaults to :8080)
go run ./cmd/carrier-gateway

# Or specify a custom address
go run ./cmd/carrier-gateway -addr :9090
```

### Curl Examples

**Get quotes for auto coverage:**
```bash
curl -s -X POST http://localhost:8080/quotes \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"demo-1","coverage_lines":["auto"]}' | jq .
```

**Get quotes with a custom timeout (ms):**
```bash
curl -s -X POST http://localhost:8080/quotes \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"demo-2","coverage_lines":["auto","homeowners"],"timeout_ms":500}' | jq .
```

**Check Prometheus metrics:**
```bash
curl -s http://localhost:8080/metrics | grep carrier_
```

## How to Run Tests

```bash
# All tests with race detector
go test -race -count=1 ./...

# Specific package
go test -race ./internal/circuitbreaker/...

# Stress test for concurrency
go test -race -run TestOrchestrator -count=5 ./internal/orchestrator/...

# Verify code formatting
gofmt -l .

# Static analysis
go vet ./...
```

## Project Structure

```
cmd/carrier-gateway/main.go     Composition root, signal handling, graceful shutdown
internal/
  domain/                       Value types, sentinel errors (stdlib only)
  ports/                        Interfaces: CarrierPort, OrchestratorPort, MetricsRecorder
  circuitbreaker/               3-state atomic circuit breaker
  ratelimiter/                  Per-carrier token bucket (x/time/rate)
  orchestrator/                 Fan-out engine + EMA hedge tracker
  adapter/                      Generic Adapter[Req,Resp], Registry, mock carriers
  metrics/                      Prometheus recorder (8 metrics)
  handler/                      HTTP handler (POST /quotes, GET /metrics)
  testutil/                     Shared test helpers (noopRecorder)
```
