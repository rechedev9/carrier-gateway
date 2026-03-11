# Delta Spec: Generic Adapter

**Change**: carrier-gateway
**Date**: 2026-03-11T18:30:00Z
**Status**: draft
**Depends On**: proposal.md

---

## Context

The generic adapter layer provides a type-safe `Adapter[Req, Resp any]` interface and a type-erased registry that stores adapters using closure capture (no reflection). This allows the orchestrator to invoke any carrier adapter through a uniform `func(context.Context, QuoteRequest) (QuoteResult, error)` function value while preserving compile-time type safety at the adapter boundary. Three mock carriers (Alpha, Beta, Gamma) are registered at startup with distinct latency and failure profiles to exercise fan-out, hedging, and circuit breaker behavior.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-ADAPT-001: Generic Adapter Interface

The `adapter` package **MUST** define a generic interface:

```go
type Adapter[Req any, Resp any] interface {
    Adapt(ctx context.Context, req Req) (Resp, error)
}
```

The type parameters `Req` and `Resp` **MUST** be unconstrained (`any`) to allow maximum flexibility.

Implementations **MUST** accept a `context.Context` as the first argument and return an error as the second return value.

#### Scenario: Typed adapter implements interface at compile time

- **GIVEN** a concrete type `AlphaAdapter` that implements `Adapt(ctx context.Context, req domain.QuoteRequest) (domain.QuoteResult, error)`
- **WHEN** the compiler checks `var _ Adapter[domain.QuoteRequest, domain.QuoteResult] = &AlphaAdapter{}`
- **THEN** compilation succeeds with no type errors

#### Scenario: Interface enforces context propagation

- **GIVEN** an `AlphaAdapter.Adapt` implementation that ignores the `ctx` argument
- **WHEN** the adapter is called with a cancelled context
- **THEN** the adapter returns an error (any non-nil error) rather than blocking — this is enforced by the mock implementation which selects on `ctx.Done()` inside the simulated sleep

---

### REQ-ADAPT-002: Type-Erased Registry via Closure Capture

The `adapter` package **MUST** provide an `AdapterRegistry` type that stores adapters as type-erased `func(context.Context, domain.QuoteRequest) (domain.QuoteResult, error)` function values, using closure capture to preserve type safety without reflection.

The registry **MUST** expose:
- `Register(carrierID string, fn func(context.Context, domain.QuoteRequest) (domain.QuoteResult, error))` — adds an adapter
- `Get(carrierID string) (func(context.Context, domain.QuoteRequest) (domain.QuoteResult, error), bool)` — retrieves an adapter

The registry **MUST NOT** use `reflect`, `interface{}` (non-`any`), or type assertions at call time — type erasure **MUST** be achieved purely via the closure.

#### Scenario: Adapter registered and retrieved by carrier ID

- **GIVEN** an `AdapterRegistry` and AlphaAdapter registered under `"alpha"`
- **WHEN** `registry.Get("alpha")` is called
- **THEN** it returns a non-nil function value and `found == true`

#### Scenario: Unregistered carrier ID returns not-found

- **GIVEN** an `AdapterRegistry` with only `"alpha"` registered
- **WHEN** `registry.Get("delta")` is called
- **THEN** it returns `nil, false`

#### Scenario: Closure captures correct type without reflection

- **GIVEN** an `AlphaAdapter` and a `BetaAdapter` with different internal state registered in the same registry
- **WHEN** both adapters are retrieved and invoked concurrently
- **THEN** each invocation routes to the correct underlying implementation; no cross-contamination of state occurs; verified by checking `result.CarrierID` matches the registry key used for retrieval

---

### REQ-ADAPT-003: Mock Carrier Implementations

The adapter package **MUST** provide three mock carrier implementations with the following profiles:

| Carrier ID | Nominal Latency | Failure Mode |
|---|---|---|
| `"alpha"` | ~50ms (jitter ±10ms) | none — always succeeds |
| `"beta"` | ~200ms (jitter ±20ms) | configurable consecutive failure injection for CB testing |
| `"gamma"` | ~800ms (jitter ±50ms) | none — slow enough to trigger hedging |

Each mock **MUST** implement `context.Context` cancellation: if the context is cancelled before the simulated sleep completes, the mock **MUST** return `context.Cause(ctx)` or `ctx.Err()` as the error.

#### Scenario: AlphaAdapter succeeds within latency profile

- **GIVEN** a non-cancelled context and `alpha` registered in the registry
- **WHEN** `alphaFn(ctx, QuoteRequest{RequestID: "r1", CoverageLines: []string{"auto"}})` is called
- **THEN** it returns a `QuoteResult{CarrierID: "alpha", Premium: <positive float64>}` and `nil` error within 100ms

#### Scenario: BetaAdapter returns error on injected failure

- **GIVEN** `BetaAdapter` is configured with `ForceFailures: 3` (next 3 calls return error)
- **WHEN** 3 consecutive calls are made
- **THEN** all 3 return a non-nil error; `errors.Is(err, domain.ErrCarrierTimeout)` returns `true` for each

#### Scenario: GammaAdapter responds in ~800ms under normal conditions

- **GIVEN** a context with a 1500ms deadline
- **WHEN** `gammaFn(ctx, req)` is called
- **THEN** it returns a valid `QuoteResult` and `nil` error with latency between 750ms and 900ms

#### Scenario: Mock adapter exits on context cancellation

- **GIVEN** a context that is cancelled after 100ms
- **WHEN** `gammaFn(ctx, req)` is called (gamma's nominal latency is ~800ms)
- **THEN** the call returns within 10ms of context cancellation (not 800ms) and returns `ctx.Err()` or `context.Canceled`

---

### REQ-ADAPT-004: No Reflection and No bare interface{}

The `adapter` package **MUST NOT** use the `reflect` package in production code.

The `adapter` package **MUST NOT** use bare `interface{}` — all `any` usage must be the Go 1.18+ `any` alias or a typed interface.

#### Scenario: Compiler enforces no interface{} usage

- **GIVEN** the `adapter` package source files
- **WHEN** `go vet ./internal/adapter/...` runs
- **THEN** no `interface{}` literals appear in non-test files (enforced via `golangci-lint` rule `forbidigo` or `gocritic`)

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority | Scenarios |
|----------------|-------|----------|-----------|
| REQ-ADAPT-001  | ADDED | MUST     | 2         |
| REQ-ADAPT-002  | ADDED | MUST     | 3         |
| REQ-ADAPT-003  | ADDED | MUST     | 4         |
| REQ-ADAPT-004  | ADDED | MUST     | 1         |

**Total Requirements**: 4
**Total Scenarios**: 10
