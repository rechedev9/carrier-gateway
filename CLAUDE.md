# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Checkpoint Rule

**At the start of every session — before reading any code or taking any action — read `log.md`.**
It is the authoritative checkpoint for what has been done and what comes next.

**After every meaningful unit of work, append an entry to `log.md`** using the format defined at the top of that file. "Meaningful unit" means: a bug fix, a new feature, a refactor, a doc change, or any work the user would want to see in a changelog. Do not log trivial reads or exploratory searches.

## Commands

```bash
# Build
go build ./...
go build ./cmd/carrier-gateway

# Run
go run ./cmd/carrier-gateway          # defaults to :8080
go run ./cmd/carrier-gateway -addr :9090

# Tests (race detector required — concurrency is core to this codebase)
go test -race -count=1 ./...
go test -race ./internal/circuitbreaker/...   # single package
go test -race -run TestOrchestrator -count=5 ./internal/orchestrator/...  # stress

# Static analysis / format
go vet ./...
gofmt -l .
gofmt -w .

# golangci-lint (if installed)
golangci-lint run ./...
```

> **Windows note:** `go test ./...` may be blocked by AppLocker policy (cannot execute compiled `.test.exe`). `go build ./...` and `go vet ./...` are unaffected.

## Architecture

Strict hexagonal layering — import direction is one-way and must never be violated:

```
domain → ports → {circuitbreaker, ratelimiter, orchestrator, adapter, metrics, handler}
```

- **`internal/domain`** — pure value types (`QuoteRequest`, `QuoteResult`, `Money`, `CoverageLine`) and sentinel errors (`ErrCircuitOpen`, `ErrRateLimitExceeded`, etc.). Imports only stdlib (`errors`, `time`). No external or internal package imports allowed here.
- **`internal/ports`** — interfaces (`CarrierPort`, `OrchestratorPort`, `MetricsRecorder`) and the `CBState` int32 type. Imports only `domain` + stdlib.
- **`internal/circuitbreaker`** — 3-state (`Closed=0/Open=1/HalfOpen=2`) machine using `atomic.Int32` CAS. HalfOpen allows exactly one probe goroutine. Prometheus gauge emitted inline on every state transition.
- **`internal/ratelimiter`** — per-carrier `golang.org/x/time/rate` token bucket. `Wait()` blocks and emits `RecordRateLimitRejection`. `TryAcquire()` is non-blocking and **silent** on false (hedge suppression must not be metered).
- **`internal/orchestrator`** — fan-out via `errgroup.WithContext`. Results channel sized `2×len(eligible)` (primary + hedge per carrier). `doneCh` pattern provides happens-before guarantee before return. Pre-filters Open CB carriers before spawning goroutines. Returns `([]QuoteResult{}, nil)` — not an error — when no carriers are eligible.
- **`internal/orchestrator/hedging.go`** — `EMATracker` per carrier using `atomic.Pointer[emaState]` CAS loop. Warm-up: suppress hedging for first 10 observations, seed p95 at `2×TimeoutHint`. `hedgeMonitor` goroutine ticks every `Config.HedgePollInterval` (default 5ms). Uses local `adapterExecFn` type alias to avoid circular imports with `internal/adapter`.
- **`internal/adapter`** — generic `Adapter[Req, Resp any]` interface. `Register[Req, Resp]` captures typed functions into type-erased closures stored in `Registry`. Zero reflection. Also contains `MockCarrier` (Alpha ~50ms/0%, Beta ~200ms/10%, Gamma ~800ms/0%).
- **`internal/metrics`** — `PrometheusRecorder` with an isolated `prometheus.NewRegistry()` (not the global default). 8 metrics covering CB state, hedges, rate limits, quotes issued/failed, and latency histogram.
- **`internal/handler`** — Go 1.22 `ServeMux`. `POST /quotes` validates input and maps empty results → HTTP 422. `GET /metrics` serves Prometheus. 30s graceful shutdown on SIGTERM/SIGINT.
- **`internal/testutil`** — `NoopRecorder` (implements `MetricsRecorder` with atomic counters). Not a `_test.go` file — importable across packages.
- **`cmd/carrier-gateway/main.go`** — composition root. Wires all layers, no global state.

## Key Invariants

- `errors.Is()` not `==` for sentinel comparisons — errors may be wrapped upstream.
- Channel buffer = `len(eligible) * 2` — accommodates worst-case primary + hedge send per carrier.
- All goroutines receive a `context.Context`; all select statements include `ctx.Done()`.
- Goroutine leak tests use `goleak.VerifyNone(t, goleak.IgnoreCurrent())` — `IgnoreCurrent()` prevents false positives from parallel test goroutines.
- `internal/metrics` uses an isolated registry. Pass it explicitly; never call `prometheus.MustRegister` on the global default.
