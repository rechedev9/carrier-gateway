# Spec: Domain Types

<!-- Added: 2026-03-11 from change: carrier-gateway -->

**Source Change**: carrier-gateway
**Merged**: 2026-03-11
**Status**: merged

---

## Context

The domain layer is the foundation of the hexagonal architecture. It defines the core data types — `QuoteRequest`, `QuoteResult`, `Carrier`, and domain sentinel errors — with zero external dependencies. Every other package depends on these types; they MUST be stable before any other package is written.

All requirements in this domain are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-DOM-001: QuoteRequest Type

The `QuoteRequest` struct **MUST** carry a non-empty `RequestID` string, a non-empty slice of `CoverageLines` strings, and an optional caller-supplied `Timeout` duration (zero value means use the orchestrator default).

The `QuoteRequest` **MUST NOT** embed any HTTP or infrastructure types — it is a pure domain value.

#### Scenario: Valid QuoteRequest construction

- **GIVEN** a caller constructs `QuoteRequest{RequestID: "req-abc123", CoverageLines: []string{"auto"}, Timeout: 5 * time.Second}`
- **WHEN** the orchestrator receives the struct
- **THEN** `req.RequestID == "req-abc123"`, `req.CoverageLines` has length 1, and `req.Timeout == 5s`

#### Scenario: Zero-value Timeout treated as default

- **GIVEN** a `QuoteRequest` is constructed with `Timeout: 0`
- **WHEN** the orchestrator checks the timeout value
- **THEN** the orchestrator substitutes its configured default timeout (e.g., `10s`) without modifying the original struct

#### Scenario: Empty CoverageLines rejected at boundary

- **GIVEN** a `QuoteRequest` arrives at the HTTP handler with `"coverage_lines": []`
- **WHEN** the handler validates the request
- **THEN** the handler returns HTTP 400 with JSON body `{"error":"INVALID_REQUEST","message":"coverage_lines must not be empty"}` and does not invoke the orchestrator

---

### REQ-DOM-002: QuoteResult Type

The `QuoteResult` struct **MUST** carry `CarrierID` (string), `Premium` (float64, positive), `CoverageLines` ([]string), `LatencyMs` (int64), and `Hedged` (bool, true if the result came from a hedge goroutine).

The `QuoteResult` **MUST NOT** contain raw error values — errors are communicated via Go's `error` return type, not embedded in the result struct.

#### Scenario: Result populated from successful carrier call

- **GIVEN** CarrierAlpha responds with premium `142.50` after `48ms`
- **WHEN** the orchestrator collects the result from the fan-out channel
- **THEN** `result.CarrierID == "alpha"`, `result.Premium == 142.50`, `result.LatencyMs == 48`, `result.Hedged == false`

#### Scenario: Hedged result marked correctly

- **GIVEN** a hedge goroutine targeting CarrierBeta responds first at `190ms`
- **WHEN** the orchestrator collects the hedge result
- **THEN** `result.Hedged == true` and `result.CarrierID == "beta"`

---

### REQ-DOM-003: Carrier Configuration Type

The `Carrier` struct **MUST** carry: `ID` (string, unique), `Name` (string), `Priority` (int, lower is higher priority), `Capabilities` ([]string — coverage lines this carrier supports), `TimeoutHint` (time.Duration — carrier's expected SLA), and `RateLimit` (int — requests per second for the token bucket).

#### Scenario: Carrier capability filtering

- **GIVEN** CarrierAlpha has `Capabilities: []string{"auto", "home"}` and CarrierGamma has `Capabilities: []string{"life"}`
- **WHEN** a `QuoteRequest` with `CoverageLines: []string{"auto"}` arrives
- **THEN** only CarrierAlpha is included in the fan-out; CarrierGamma is excluded

#### Scenario: Priority used for hedge candidate selection

- **GIVEN** CarrierAlpha has `Priority: 1` and CarrierBeta has `Priority: 2`, both have identical p95 latency
- **WHEN** the hedge monitor selects a fallback carrier
- **THEN** CarrierAlpha (lower priority number = higher priority) is selected as the hedge target

---

### REQ-DOM-004: Sentinel Domain Errors

The domain package **MUST** export the following sentinel errors:

| Sentinel | Meaning |
|---|---|
| `ErrCarrierTimeout` | Carrier did not respond within the allotted time |
| `ErrCarrierUnavailable` | Circuit breaker is Open for this carrier |
| `ErrRateLimitExceeded` | Token bucket exhausted for this carrier |
| `ErrInvalidRequest` | QuoteRequest failed validation |

All orchestrator and adapter errors **MUST** be wrapped with `fmt.Errorf("...: %w", sentinelErr)` to preserve `errors.Is` unwrapping.

#### Scenario: ErrCarrierUnavailable propagates correctly

- **GIVEN** CarrierBeta's circuit breaker is in the Open state
- **WHEN** the orchestrator attempts to dispatch a goroutine for CarrierBeta
- **THEN** the goroutine immediately sends `fmt.Errorf("carrier beta: %w", ErrCarrierUnavailable)` to the results channel without invoking the adapter

#### Scenario: Error wrapping preserves sentinel identity

- **GIVEN** an error returned by the adapter is `fmt.Errorf("alpha adapter: %w", ErrCarrierTimeout)`
- **WHEN** caller uses `errors.Is(err, ErrCarrierTimeout)`
- **THEN** `errors.Is` returns `true`

#### Scenario: Unrecognized adapter error does not panic

- **GIVEN** a mock carrier adapter returns a non-sentinel `errors.New("unexpected internal error")`
- **WHEN** the orchestrator receives this error on the results channel
- **THEN** the error is logged with `slog.Error` carrying `carrier_id` and `request_id` fields, and that carrier's result is omitted from the response — no panic occurs

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority | Scenarios |
|----------------|-------|----------|-----------|
| REQ-DOM-001    | ADDED | MUST     | 3         |
| REQ-DOM-002    | ADDED | MUST     | 2         |
| REQ-DOM-003    | ADDED | MUST     | 2         |
| REQ-DOM-004    | ADDED | MUST     | 3         |

**Total Requirements**: 4
**Total Scenarios**: 10
