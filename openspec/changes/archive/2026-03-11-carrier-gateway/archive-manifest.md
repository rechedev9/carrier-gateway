# Archive Manifest: carrier-gateway

**Archived**: 2026-03-11
**Verdict**: PASS_WITH_WARNINGS
**Tasks Completed**: 33/33
**Specs Merged**: domain-types, circuit-breaker, adaptive-hedging, rate-limiter, fan-out-orchestrator, generic-adapter, http-handler
**Warnings**: 1 (test execution blocked by Windows AppLocker policy — environment constraint, not a code defect)

---

## Change Summary

Built a production-grade Multi-Carrier Quote Aggregation Engine in Go 1.22 from a greenfield state. The system is an HTTP binary (`POST /quotes`, `GET /metrics`) structured as a hexagonal (ports-and-adapters) application with strict layer isolation. It demonstrates five advanced engineering capabilities simultaneously: fan-out/fan-in concurrency via `errgroup`, per-carrier circuit breakers with `sync/atomic` state machines, adaptive EMA-based hedging, generic type-safe adapters using closure capture (no reflection), and Prometheus-instrumented per-carrier rate limiting.

The change was driven by the need to showcase a demonstrable, curl-able binary that a CTO could evaluate directly — every concurrency system is observable via the `/metrics` endpoint within a single request cycle using the three mock carriers (Alpha, Beta, Gamma) with distinct latency/failure profiles.

---

## Key Decisions

| # | Decision | Choice | Rationale |
|---|---|---|---|
| 1 | Circuit breaker implementation | Custom `sync/atomic` 3-state machine | Direct Prometheus gauge emission on state transitions without adapter wrapping; demonstrates mastery; `atomic.Int32` (Go 1.19+) avoids locks |
| 2 | Rate limiter | `golang.org/x/time/rate` token bucket per carrier | Production-proven, official extended stdlib family; frees complexity budget for CB and hedging |
| 3 | Adapter type erasure | Closure capture `func(ctx, QuoteRequest) (QuoteResult, error)` | Zero runtime overhead; full type safety at generic boundary; avoids reflection fragility |
| 4 | Fan-out lifecycle | `golang.org/x/sync/errgroup` with `WithContext` | Idiomatic structured concurrency; propagates context cancellation; already in extended stdlib ecosystem |
| 5 | EMA p95 granularity | Per-carrier EMA, α=0.1, warm-up suppression (N<10) | Global EMA conflates fast/slow carriers; `atomic.Pointer[emaState]` is lock-free for read-heavy workloads |
| 6 | Hedge first-arrival wins | First result per coverage bundle wins regardless of source | Minimises latency; avoids extra coordination overhead |
| 7 | HalfOpen probe concurrency | Strictly 1 via `atomic.Int32` CAS | Standard practice; prevents probe amplification; race-condition-free |
| 8 | HTTP router | `net/http` stdlib `ServeMux` (Go 1.22) | Go 1.22 handles method-scoped routes natively; no third-party router dependency |
| 9 | Logging | `log/slog` structured logger | stdlib (Go 1.21+); all log lines carry `carrier_id` + `request_id`; zero external logging dependency |
| 10 | No-eligible carriers result | Return `([]QuoteResult{}, nil)` — not an error | Handler maps empty slice to HTTP 422; cleaner separation of orchestrator and HTTP concerns |
| 11 | Fan-out channel buffer | `len(eligible) * 2` | Accommodates worst case: all primaries + all hedges sending to same channel; eliminates deadlock risk |

---

## Files Created

- `cmd/carrier-gateway/main.go` — composition root, HTTP server, SIGTERM/SIGINT graceful shutdown
- `internal/domain/quote.go` — QuoteRequest, QuoteResult, Money, CoverageLines typed constants
- `internal/domain/carrier.go` — Carrier, CarrierConfig, RateLimitConfig
- `internal/domain/errors.go` — sentinel errors (ErrCircuitOpen, ErrRateLimitExceeded, ErrNoEligibleCarriers, ErrCarrierTimeout, ErrCarrierUnavailable, ErrInvalidRequest)
- `internal/ports/quote_port.go` — CarrierPort, OrchestratorPort interfaces
- `internal/ports/metrics_port.go` — MetricsRecorder interface, CBState type and constants
- `internal/circuitbreaker/breaker.go` — 3-state CB with atomic.Int32, CAS transitions, Prometheus emission
- `internal/ratelimiter/limiter.go` — per-carrier token bucket, Wait/TryAcquire, metric emission
- `internal/orchestrator/orchestrator.go` — fan-out/fan-in engine, filterEligibleCarriers, dedup+sort, structured logging
- `internal/orchestrator/hedging.go` — EMATracker (atomic.Pointer), hedgeMonitor goroutine, selectHedgeCandidate
- `internal/adapter/adapter.go` — generic Adapter[Req,Resp] interface, type-erased Registry via closure
- `internal/adapter/mock_carrier.go` — MockCarrier, Alpha/Beta/Gamma constructors, RegisterMockCarrier
- `internal/metrics/prometheus.go` — PrometheusRecorder implementing MetricsRecorder, all 8 metric fields
- `internal/handler/http.go` — HTTP handler, validateQuoteRequest, domain-error→HTTP mapping, graceful shutdown
- `internal/testutil/recorder.go` — noopRecorder test stub (non-test, importable across packages)
- `go.mod`, `go.sum` — module manifest and checksums

---

## Files Modified

None — greenfield project (all files created new).

---

## Test Coverage

- 54 test functions across 8 test files
- Test files: `orchestrator_test.go`, `hedging_test.go`, `http_test.go`, `limiter_test.go`, `breaker_test.go`, `adapter_test.go`, `mock_carrier_test.go`, `prometheus_test.go`
- Patterns: table-driven, dependency injection, goleak goroutine-leak detection, `-race` flag coverage

---

## Build Health at Archive

| Check | Status |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | PASS |
| `golangci-lint` | SKIPPED (not installed) |
| `go test ./...` | SKIPPED (Windows AppLocker policy) |

---

## Outstanding Items (Non-blocking)

- Tests verified by `sdd-apply` phase 5 with `-race` flag; cannot re-run on this machine due to AppLocker. Resolve by running in a Linux/macOS CI environment.
