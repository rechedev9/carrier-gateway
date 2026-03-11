# Spec: Circuit Breaker

<!-- Added: 2026-03-11 from change: carrier-gateway -->

**Source Change**: carrier-gateway
**Merged**: 2026-03-11
**Status**: merged

---

## Context

The circuit breaker is a per-carrier state machine with three states: Closed (normal operation), Open (rejecting calls), and HalfOpen (probing for recovery). It is implemented using `sync/atomic` for lock-free state transitions and emits Prometheus gauge metrics on every transition. It is the primary resilience mechanism preventing cascading failures when a carrier degrades.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-CB-001: Three-State State Machine

The circuit breaker **MUST** implement exactly three states: `Closed` (value 0), `Open` (value 1), and `HalfOpen` (value 2). These constants **MUST** be exported from the `circuitbreaker` package.

State transitions **MUST** follow this directed graph:

```
Closed  → Open      (on consecutive failure threshold reached)
Open    → HalfOpen  (on reset timeout elapsed)
HalfOpen → Closed   (on probe success)
HalfOpen → Open     (on probe failure)
```

No other transitions are permitted.

#### Scenario: Closed to Open transition on failure threshold

- **GIVEN** a circuit breaker for CarrierBeta is in state `Closed` with `FailureThreshold: 5`
- **WHEN** 5 consecutive calls to `breaker.RecordFailure()` are made
- **THEN** `breaker.State()` returns `Open` and the Prometheus gauge `carrier_circuit_breaker_state{carrier_id="beta"}` is set to `1`

#### Scenario: Successful call resets consecutive failure counter

- **GIVEN** a circuit breaker in state `Closed` with 4 recorded consecutive failures
- **WHEN** `breaker.RecordSuccess()` is called
- **THEN** the consecutive failure counter resets to 0, the breaker remains `Closed`, and the next failure restarts the counter from 1

#### Scenario: Invalid state transition is impossible

- **GIVEN** a circuit breaker in state `Closed`
- **WHEN** internal code attempts to transition directly to `HalfOpen` (skipping `Open`)
- **THEN** the transition is rejected by the state machine (compile-time constraint via unexported transition method; no runtime path allows Closed → HalfOpen directly)

---

### REQ-CB-002: Open State — Call Rejection

When the circuit breaker is in state `Open`, **MUST** reject all incoming calls by returning `ErrCarrierUnavailable` immediately without invoking the carrier adapter.

The breaker **MUST** track the timestamp at which it entered `Open` state so the reset timeout can be evaluated.

#### Scenario: Open breaker rejects call instantly

- **GIVEN** CarrierBeta's circuit breaker is in state `Open`
- **WHEN** the orchestrator calls `breaker.Allow()`
- **THEN** `breaker.Allow()` returns `false` within 1 microsecond (no I/O); the adapter is never called

#### Scenario: Open breaker transitions to HalfOpen after reset timeout

- **GIVEN** CarrierBeta's circuit breaker entered `Open` state at time T, and `ResetTimeout` is configured as `10s`
- **WHEN** `breaker.Allow()` is called at time T+11s
- **THEN** the breaker transitions to `HalfOpen`, `breaker.Allow()` returns `true` for exactly one probe goroutine, and the Prometheus gauge `carrier_circuit_breaker_state{carrier_id="beta"}` is set to `2`

---

### REQ-CB-003: HalfOpen State — Single Probe Enforcement

When the circuit breaker is in state `HalfOpen`, **MUST** allow exactly one concurrent probe call (`HalfOpenMaxConc = 1`). All additional concurrent callers **MUST** receive `false` from `breaker.Allow()` until the probe completes.

The single-probe invariant **MUST** be enforced using `sync/atomic` compare-and-swap, not a mutex, to avoid priority inversion.

#### Scenario: Only one probe allowed concurrently in HalfOpen

- **GIVEN** CarrierBeta's circuit breaker is in state `HalfOpen`
- **WHEN** three goroutines simultaneously call `breaker.Allow()`
- **THEN** exactly one returns `true` and two return `false`; verified by table-driven test with `-race` flag

#### Scenario: Probe success closes the breaker

- **GIVEN** CarrierBeta's circuit breaker is in state `HalfOpen` and one probe is in flight
- **WHEN** `breaker.RecordSuccess()` is called for the probe
- **THEN** the breaker transitions to `Closed`, the Prometheus gauge is set to `0`, and `breaker.Allow()` returns `true` for subsequent callers

#### Scenario: Probe failure re-opens the breaker

- **GIVEN** CarrierBeta's circuit breaker is in state `HalfOpen` and one probe is in flight
- **WHEN** `breaker.RecordFailure()` is called for the probe
- **THEN** the breaker transitions back to `Open`, the Prometheus gauge is set to `1`, and the open-state entry timestamp is reset to now

---

### REQ-CB-004: Prometheus Gauge Emission on State Transition

On every state transition, the circuit breaker **MUST** set the Prometheus gauge `carrier_circuit_breaker_state{carrier_id=<id>}` to the numeric state value: `0` (Closed), `1` (Open), `2` (HalfOpen).

The gauge **MUST** be updated atomically with the state transition — there **MUST NOT** be an observable window where the in-memory state and the gauge value disagree.

#### Scenario: Gauge reflects Open state immediately after transition

- **GIVEN** CarrierBeta's breaker is `Closed` and the gauge value is `0`
- **WHEN** the 5th consecutive failure triggers the `Open` transition
- **THEN** within the same function call (before returning to the caller), `carrier_circuit_breaker_state{carrier_id="beta"}` equals `1`

#### Scenario: Metrics endpoint exposes state for all three carriers

- **GIVEN** Alpha is `Closed` (0), Beta is `Open` (1), Gamma is `HalfOpen` (2)
- **WHEN** `GET /metrics` is scraped
- **THEN** the response body contains all three lines:
  ```
  carrier_circuit_breaker_state{carrier_id="alpha"} 0
  carrier_circuit_breaker_state{carrier_id="beta"} 1
  carrier_circuit_breaker_state{carrier_id="gamma"} 2
  ```

---

### REQ-CB-005: Configurable Thresholds

The circuit breaker **MUST** accept a `Config` struct at construction time with at minimum: `FailureThreshold int`, `ResetTimeout time.Duration`. Default values **SHOULD** be: `FailureThreshold: 5`, `ResetTimeout: 30s`.

The circuit breaker **MUST NOT** use global or package-level state — each `CircuitBreaker` instance is independent.

#### Scenario: Custom failure threshold respected

- **GIVEN** a circuit breaker constructed with `Config{FailureThreshold: 3, ResetTimeout: 5s}`
- **WHEN** 3 consecutive failures are recorded
- **THEN** the breaker transitions to `Open` after exactly the 3rd failure, not the 5th

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority | Scenarios |
|----------------|-------|----------|-----------|
| REQ-CB-001     | ADDED | MUST     | 3         |
| REQ-CB-002     | ADDED | MUST     | 2         |
| REQ-CB-003     | ADDED | MUST     | 3         |
| REQ-CB-004     | ADDED | MUST     | 2         |
| REQ-CB-005     | ADDED | MUST/SHOULD | 1      |

**Total Requirements**: 5
**Total Scenarios**: 11
