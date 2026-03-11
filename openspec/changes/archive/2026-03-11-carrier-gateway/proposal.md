# Proposal: Carrier Gateway

**Change ID**: carrier-gateway
**Date**: 2026-03-11T18:00:00Z
**Status**: draft

---

## Intent

Build a production-grade Multi-Carrier Quote Aggregation Engine in Go from scratch that demonstrates five advanced engineering capabilities — fan-out/fan-in concurrency, per-carrier circuit breakers, adaptive EMA-based hedging, generic type-safe adapters, and Prometheus-instrumented rate limiting — packaged as a runnable HTTP binary that a CTO can curl directly to observe all systems in action.

## Scope

### In Scope

- **HTTP binary** with two endpoints: `POST /quotes` (quote fan-out) and `GET /metrics` (Prometheus scrape)
- **Hexagonal architecture** with strict layer isolation: `domain/`, `ports/`, `orchestrator/`, `circuitbreaker/`, `ratelimiter/`, `adapter/`, `metrics/`, `handler/`, `cmd/`
- **Fan-out/fan-in orchestrator** that dispatches concurrent goroutine per eligible carrier and collects results within caller-supplied timeout
- **Custom 3-state circuit breaker** (Closed → Open → HalfOpen → Closed) per carrier with direct Prometheus gauge emission on every state transition
- **Adaptive hedging** driven by per-carrier EMA p95 latency tracker — fires a secondary goroutine to a fallback carrier when the primary exceeds its hedge threshold
- **Generic `Adapter[Req, Resp any]`** interface with a type-erased registry using closure capture (no reflection)
- **3 mock carriers** with distinct in-process latency/failure profiles: Alpha (~50ms, reliable), Beta (~200ms, occasional failures), Gamma (~800ms, triggers hedging)
- **Per-carrier token-bucket rate limiter** using `golang.org/x/time/rate`
- **Prometheus metrics**: circuit breaker state gauges, p95 EMA gauges, quote latency histograms, fan-out duration histograms, request/hedge/CB transition/rate-limit counters
- **Structured logging** with `log/slog` — every log line carries `carrier_id` and `request_id`
- **Table-driven tests** with `-race` flag coverage for all concurrent components
- Graceful HTTP server shutdown on `SIGTERM`/`SIGINT`

### Out of Scope

- Real outbound HTTP calls to carrier sandbox APIs — per clarification, in-process simulation (time.Sleep + error injection) is used; swapping in real adapters is trivially possible via the Adapter interface but is not implemented here
- Database or persistent storage — all state (CB counters, EMA values, rate-limit buckets) is in-memory per-process
- Authentication or authorization on the HTTP server — this is a demo binary, not a production service; auth is a separate concern
- Multi-instance coordination or distributed state — circuit breaker and EMA state are local to the process; distributed CB (e.g., via Redis) is explicitly out of scope
- Config hot-reload — carrier configuration is static at startup; a file-watch mechanism is deferred
- Production deployment manifests (Dockerfile, Kubernetes YAML, Terraform) — the deliverable is source code and a `go run` invocation

---

## Approach

The system is structured as a **hexagonal (ports-and-adapters) application** where the domain layer has zero external dependencies, the ports layer defines inbound and outbound interfaces, and all infrastructure (HTTP, Prometheus, mock carriers) lives in adapter packages. This ensures that swapping out any infrastructure component (e.g., replacing mock carriers with real HTTP clients) requires no changes to the orchestrator or domain.

The implementation order follows **hexagonal layers outward** — domain types first, then port interfaces, then infrastructure components (circuit breaker, rate limiter), then the orchestrator core engine, then adapters and metrics, then the HTTP handler, and finally the composition root in `cmd/`. This order prevents circular dependencies and ensures each layer compiles cleanly before the next is added.

The orchestrator runs one goroutine per eligible carrier in a fan-out, collects results from a shared channel, and runs a concurrent hedge-monitor goroutine that polls pending carriers every 5 ms. If a carrier's elapsed time exceeds its EMA-derived hedge threshold, the monitor fires a hedge goroutine to the best-available fallback carrier (lowest p95, with priority as tiebreak). Both primary and hedge goroutines send to the same results channel; the first result per coverage bundle wins and duplicates are discarded.

Per clarification from the user:
- **HTTP server binary** chosen over library packaging — a runnable binary the CTO can `curl` is more impressive and immediately verifiable
- **3 mock carriers** chosen — Alpha, Beta, Gamma — to make every concurrency system (fan-out, hedging, CB, rate limiting) observable within a single demo request
- **In-process simulation** chosen over real HTTP calls — hermetically testable, zero credentials, identical architectural pattern

### Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Circuit breaker implementation | Custom `sync/atomic` state machine | Direct Prometheus gauge emission on state transitions, no adapter wrapping; demonstrates mastery to CTO audience |
| Rate limiter implementation | `golang.org/x/time/rate` (official Go extended library) | Production-proven, token-bucket semantics, frees complexity budget for CB and hedging |
| EMA tracking granularity | Per-carrier EMA p95 | Global EMA would conflate fast and slow carriers, causing hedging to fire incorrectly |
| Hedge candidate selection | Lowest p95 carrier, `Carrier.Priority` as tiebreak | Statistically optimal; adapts to runtime conditions rather than static config |
| Adapter type erasure | Closure capture (`func(QuoteRequest) (QuoteResult, error)`) | Avoids reflection, preserves type safety at the generic boundary, zero runtime overhead |
| Mock carrier count | 3 carriers (Alpha/Beta/Gamma) | Minimum count to simultaneously demonstrate fan-out, hedging trigger, and CB failure injection |
| Server vs library | HTTP binary (`POST /quotes`, `GET /metrics`) | Directly runnable and curl-demoable; CTO can observe behaviour without writing calling code |
| Dependency on `errgroup` | `golang.org/x/sync/errgroup` | Idiomatic Go fan-out with clean cancellation propagation; already in the extended stdlib ecosystem |

---

## Affected Areas

| Module / Area | File Path | Change Type | Risk Level |
|---|---|---|---|
| Entry point | `C:/Users/Reche/Desktop/gopro/cmd/carrier-gateway/main.go` | create | low |
| Domain types | `C:/Users/Reche/Desktop/gopro/internal/domain/quote.go` | create | low |
| Domain types | `C:/Users/Reche/Desktop/gopro/internal/domain/carrier.go` | create | low |
| Domain types | `C:/Users/Reche/Desktop/gopro/internal/domain/errors.go` | create | low |
| Port interfaces | `C:/Users/Reche/Desktop/gopro/internal/ports/quote_port.go` | create | low |
| Port interfaces | `C:/Users/Reche/Desktop/gopro/internal/ports/metrics_port.go` | create | low |
| Orchestrator core | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/orchestrator.go` | create | high |
| Adaptive hedging | `C:/Users/Reche/Desktop/gopro/internal/orchestrator/hedging.go` | create | medium |
| Circuit breaker | `C:/Users/Reche/Desktop/gopro/internal/circuitbreaker/breaker.go` | create | medium |
| Rate limiter | `C:/Users/Reche/Desktop/gopro/internal/ratelimiter/limiter.go` | create | medium |
| Generic adapter | `C:/Users/Reche/Desktop/gopro/internal/adapter/adapter.go` | create | medium |
| Mock carriers | `C:/Users/Reche/Desktop/gopro/internal/adapter/mock_carrier.go` | create | low |
| Prometheus metrics | `C:/Users/Reche/Desktop/gopro/internal/metrics/prometheus.go` | create | low |
| HTTP handler | `C:/Users/Reche/Desktop/gopro/internal/handler/http.go` | create | low |
| Go module manifest | `C:/Users/Reche/Desktop/gopro/go.mod` | create | low |
| Go module lock | `C:/Users/Reche/Desktop/gopro/go.sum` | create | low |

**Total files affected**: 16
**New files**: 16
**Modified files**: 0
**Deleted files**: 0

> **Size note**: 16 new files across 8 packages qualifies as a large change. However, splitting is not recommended here because all packages form a single tightly coupled system — the orchestrator cannot be built without the CB, rate limiter, and adapter packages, and the HTTP handler cannot be built without the orchestrator. The hexagonal layering already provides natural implementation checkpoints (see implementation order in Approach). Each layer compiles independently, so the work can be reviewed layer by layer without splitting the change name.

---

## Risks

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| Race conditions in orchestrator fan-out | medium | high | Run all tests with `go test -race ./...`; use channels for result passing, not shared maps; use `sync/atomic` for CB state |
| Generic `Adapter[T,U]` type-erasure bug (wrong closure capture) | low | high | Write dedicated unit tests for adapter registry with multiple concrete types; use `t.Run` table-driven tests |
| EMA warm-up period causes premature hedging | medium | medium | Suppress hedging for the first N observations (N=10); use `2 × TimeoutHint` as the initial p95 seed; specify this in spec |
| Circuit breaker half-open probe race (two probes fire simultaneously) | low | medium | Enforce `HalfOpenMaxConc=1` using `sync/atomic` compare-and-swap; test with `go test -race` |
| Prometheus metrics cardinality blow-up if carrier count grows | low | low | At 3 mock carriers, cardinality is trivial; document that label design scales linearly with carrier count |
| Goroutine leak if results channel is not fully drained | low | high | Use `context.WithTimeout` for every fan-out; ensure collector goroutine always reads until channel closes; verify with `goleak` in tests |
| HTTP handler accepting unbounded request body | low | medium | Apply `http.MaxBytesReader` in handler; validate `QuoteRequest` fields before passing to orchestrator |

**Overall Risk Level**: medium

The primary risk is concurrency correctness in the orchestrator. Mitigation through the race detector and careful channel/atomic discipline is standard Go practice and well-understood.

---

## Rollback Plan

This is a greenfield project — there is no existing production system to roll back to. Rollback means reverting to the pre-implementation state: an empty repository with only SDD scaffolding.

### Steps to Rollback

1. Identify the last commit before implementation began (the SDD scaffolding commit):
   ```
   git log --oneline | grep -i "sdd\|init\|scaffold"
   ```
2. Reset the working tree to that commit (non-destructive: create a rollback branch first):
   ```
   git checkout -b rollback/carrier-gateway-$(date +%Y%m%d)
   git reset --hard <scaffold-commit-sha>
   ```
3. Delete all generated source directories if reset is not sufficient:
   ```
   rm -rf cmd/ internal/ go.mod go.sum
   ```
4. Verify only SDD scaffolding remains:
   ```
   ls  # should show only: openspec/ AGENTS.md
   ```

### Rollback Verification

- `ls` at project root shows only `openspec/` and `AGENTS.md` (no `cmd/`, `internal/`, `go.mod`)
- `git status` shows a clean working tree at the scaffold commit
- The `openspec/changes/carrier-gateway/` directory is preserved (it contains specs and this proposal — these are not rolled back unless explicitly requested)

---

## Dependencies

### Internal Dependencies

- Domain types (`internal/domain/`) must be created before any other package; they have zero external dependencies
- Port interfaces (`internal/ports/`) must be created before the orchestrator and handler
- Circuit breaker and rate limiter packages must be created before the orchestrator that composes them
- Adapter package must be created after domain types are stable

### External Dependencies

| Package | Version | Purpose | Already Installed |
|---|---|---|---|
| `golang.org/x/sync` | latest (v0.7+) | `errgroup` for fan-out goroutine lifecycle management | no |
| `golang.org/x/time` | latest (v0.5+) | `rate.Limiter` for per-carrier token-bucket rate limiting | no |
| `github.com/prometheus/client_golang` | v1.19+ | Prometheus metrics instrumentation and `/metrics` HTTP handler | no |

### Infrastructure Dependencies

- Database migration needed: no
- New environment variables: none (all configuration is compiled in or passed as startup flags; see deferred questions on config format)
- New services: none (in-process simulation; no external carrier APIs, no message broker, no cache)

---

## Success Criteria

All of the following must be true for this change to be considered complete:

- [ ] `go build ./...` succeeds with zero errors
- [ ] `go vet ./...` passes with zero diagnostics
- [ ] `go test -race ./...` passes with zero failures and zero detected race conditions
- [ ] `gofmt -l .` produces no output (all files are gofmt-clean)
- [ ] `curl -s -X POST http://localhost:8080/quotes -d '{"request_id":"demo-1","coverage_lines":["auto"]}' | jq .` returns a JSON array of at least 2 `QuoteResult` objects
- [ ] `curl -s http://localhost:8080/metrics` returns Prometheus text exposition containing `carrier_circuit_breaker_state`, `carrier_p95_latency_ms`, `carrier_requests_total`, and `hedge_requests_total`
- [ ] Sending 10 sequential requests to `POST /quotes` with the 800ms Gamma carrier included causes at least one `hedge_requests_total` counter increment (observable via `/metrics`)
- [ ] Injecting 5 consecutive failures into CarrierBeta causes its circuit breaker to transition to Open state, observable via `carrier_circuit_breaker_state{carrier_id="beta"} == 2`
- [ ] Every log line emitted to stderr during a quote request contains both `request_id` and `carrier_id` as structured `slog` fields (no unstructured `fmt.Println` or `log.Printf` calls in production code)
- [ ] No file in `internal/` or `cmd/` exceeds 600 lines
- [ ] No use of `interface{}` (bare, non-`any`) in any production file
- [ ] All errors returned from domain and orchestrator functions are wrapped with `fmt.Errorf("%w", ...)` or sentinel errors — no silent error discards
- [ ] Rollback plan has been read and the rollback branch point identified

---

## Open Questions

The following questions were deferred from exploration. They are not blocking for the proposal but must be answered before or during the spec/design phases:

- **EMA warm-up strategy**: Should hedging be suppressed for the first N observations (recommended: N=10), or should the initial p95 seed be set to `2 × CarrierConfig.TimeoutHint`? Both can be combined.
- **Request fan-out scope**: Does every `QuoteRequest` fan out to ALL registered carriers, or only those whose `Carrier.Capabilities` intersect `request.CoverageLines`? (Recommended: filter by capabilities.)
- **Result ordering**: Should `[]QuoteResult` be sorted by `Premium` ascending, or by a composite score (latency + premium)? (Recommended: premium ascending for simplicity.)
- **CB `HalfOpenMaxConc`**: Is the half-open probe count strictly 1, or a configurable parameter? (Recommended: 1, hardcoded for this demo.)
- **Graceful shutdown drain window**: How long does the server wait for in-flight requests before force-exiting after SIGTERM? (Recommended: 30 seconds.)
- **Quota accounting on hedge**: If a hedge fires and both primary and hedge return results, does the hedge response take precedence over the primary, or does the first arrival win regardless of source? (Recommended: first arrival wins.)

---

**Next Step**: Review and approve this proposal, then run `sdd-spec` and `sdd-design` (can run in parallel).
