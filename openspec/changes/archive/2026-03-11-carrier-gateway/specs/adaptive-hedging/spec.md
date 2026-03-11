# Delta Spec: Adaptive Hedging

**Change**: carrier-gateway
**Date**: 2026-03-11T18:30:00Z
**Status**: draft
**Depends On**: proposal.md

---

## Context

Adaptive hedging fires a redundant goroutine to a fallback carrier when the primary carrier for a given coverage line exceeds its EMA-derived p95 latency threshold. The EMA (exponential moving average) tracker updates per successful response and tracks per-carrier latency. The hedge monitor runs as a concurrent goroutine alongside the fan-out, polling pending carriers every 5ms. This spec covers the EMA tracker, hedge monitor, hedge candidate selection, and quota accounting on hedge results.

Open questions resolved for this spec (per proposal recommendations):
- EMA warm-up: suppress hedging for the first 10 observations AND use `2 × Carrier.TimeoutHint` as the initial p95 seed value.
- First arrival wins: when both primary and hedge return results for the same carrier/coverage bundle, the first arrival is kept and the duplicate is discarded.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-HEDGE-001: Per-Carrier EMA p95 Tracker

The system **MUST** maintain a per-carrier exponential moving average that approximates the p95 latency. The EMA update formula **MUST** use a smoothing factor `α = 2 / (N+1)` where N is a configurable window size (default: 20 observations).

The tracker **MUST** be safe for concurrent use from multiple goroutines without a global lock on the hot read path. An `sync/atomic` or `sync/RWMutex` approach is acceptable.

The p95 estimate **MUST** be initialized to `2 × Carrier.TimeoutHint` before any observations are recorded.

#### Scenario: EMA initialized to 2× TimeoutHint

- **GIVEN** CarrierGamma has `TimeoutHint: 400ms`
- **WHEN** the EMA tracker for Gamma is constructed
- **THEN** `tracker.P95()` returns `800ms` before any observation is recorded

#### Scenario: EMA updates toward observed latency over time

- **GIVEN** CarrierAlpha's EMA is initialized to `100ms` (2 × 50ms hint) with window N=20 (α ≈ 0.095)
- **WHEN** 30 consecutive observations of `48ms` are recorded
- **THEN** `tracker.P95()` converges to within 5ms of `48ms`

#### Scenario: Concurrent observation recording is race-free

- **GIVEN** an EMA tracker for CarrierBeta is shared by 10 concurrent goroutines each recording latency observations
- **WHEN** the test runs with `-race` flag for 1000 iterations
- **THEN** the race detector reports zero data races

---

### REQ-HEDGE-002: Warm-Up Suppression

The hedge monitor **MUST NOT** fire a hedge goroutine for a carrier that has fewer than 10 recorded observations (the warm-up period). During warm-up, the initial `2 × TimeoutHint` seed is used internally but hedging is suppressed entirely.

#### Scenario: No hedging during warm-up period

- **GIVEN** CarrierGamma has recorded 8 observations and its p95 EMA is still at the seed value
- **WHEN** a request takes 900ms and exceeds the hedge threshold
- **THEN** the hedge monitor does NOT fire a hedge goroutine for Gamma; `hedge_requests_total` counter is NOT incremented

#### Scenario: Hedging activates after warm-up completes

- **GIVEN** CarrierGamma has recorded exactly 10 observations
- **WHEN** the 11th request to Gamma exceeds the p95 hedge threshold
- **THEN** the hedge monitor IS allowed to fire a hedge goroutine; `hedge_requests_total` counter increments by 1

---

### REQ-HEDGE-003: Hedge Monitor Goroutine

The orchestrator **MUST** start exactly one hedge monitor goroutine per fan-out call. The monitor **MUST** poll pending carriers every 5ms and compare each pending carrier's elapsed time against its EMA p95 threshold.

The hedge monitor **MUST** exit when the parent context is cancelled or when all pending carriers have completed (no carriers left to monitor).

The hedge monitor **MUST NOT** fire more than one hedge goroutine per primary carrier per fan-out call (one hedge per primary slot).

#### Scenario: Hedge monitor fires for slow carrier

- **GIVEN** CarrierGamma has p95 EMA of `300ms` and has been pending for `310ms`
- **WHEN** the hedge monitor polls at 5ms intervals
- **THEN** the monitor fires a hedge goroutine to the best available fallback carrier within the next 5ms poll cycle

#### Scenario: Hedge monitor exits when context cancels

- **GIVEN** the fan-out context has a 500ms timeout and the hedge monitor is running
- **WHEN** the context expires at 500ms
- **THEN** the hedge monitor goroutine exits within one poll cycle (≤5ms); `goleak.VerifyNone` reports zero leaked goroutines

#### Scenario: Second hedge not fired for same primary slot

- **GIVEN** a hedge goroutine was already fired for CarrierGamma in this fan-out call
- **WHEN** the hedge monitor polls again and Gamma is still pending
- **THEN** no second hedge goroutine is fired for Gamma; `hedge_requests_total` is incremented at most once per carrier per fan-out call

---

### REQ-HEDGE-004: Hedge Candidate Selection

The hedge monitor **MUST** select the fallback carrier by choosing the eligible carrier with the lowest p95 EMA latency that is not the current primary carrier for this slot and whose circuit breaker is not Open.

The `Carrier.Priority` field **MUST** be used as a tiebreak when two candidates have identical p95 EMA values (lower priority number wins).

The hedge monitor **MUST NOT** fire a hedge to a carrier whose circuit breaker is `Open` or whose rate limit bucket is exhausted.

#### Scenario: Lowest p95 carrier selected as hedge target

- **GIVEN** CarrierAlpha has p95=50ms, CarrierBeta has p95=200ms, and the primary slot is CarrierGamma (p95=800ms)
- **WHEN** the hedge monitor selects a fallback for Gamma's slot
- **THEN** CarrierAlpha (lowest p95 among non-primary carriers) is selected as the hedge target

#### Scenario: Open circuit breaker carrier is skipped for hedging

- **GIVEN** CarrierAlpha's circuit breaker is `Open` and CarrierBeta is `Closed`
- **WHEN** the hedge monitor selects a fallback for CarrierGamma
- **THEN** CarrierBeta is selected; CarrierAlpha is not considered

#### Scenario: No available hedge candidate — no goroutine fired

- **GIVEN** all non-primary carriers have `Open` circuit breakers
- **WHEN** the hedge monitor would normally fire a hedge for CarrierGamma
- **THEN** no hedge goroutine is fired; the monitor logs a `slog.Warn` with `"no eligible hedge candidate"` and `request_id`; `hedge_requests_total` is NOT incremented

---

### REQ-HEDGE-005: First-Arrival Result Wins

When both a primary goroutine and a hedge goroutine return results for the same carrier coverage slot, the orchestrator **MUST** keep the first result that arrives in the channel and **MUST** discard the second result.

The discard **MUST** be logged at `slog.Debug` level with `carrier_id` and `request_id` fields.

#### Scenario: Primary arrives before hedge — hedge discarded

- **GIVEN** primary goroutine for Gamma's slot returns at 350ms and hedge goroutine returns at 400ms
- **WHEN** the collector processes both channel messages
- **THEN** the slot is filled at 350ms; the 400ms result is discarded; final slice contains one result for this coverage slot

#### Scenario: Hedge arrives before primary — primary discarded

- **GIVEN** the hedge goroutine targeting CarrierAlpha returns at 180ms and the primary CarrierGamma returns at 820ms
- **WHEN** the collector processes both
- **THEN** the AlphaHedge result (marked `Hedged: true`) is retained; the late Gamma result is discarded

---

### REQ-HEDGE-006: Prometheus Instrumentation for Hedging

The system **MUST** expose a `hedge_requests_total{carrier_id=<fallback_carrier_id>}` Prometheus counter that increments each time a hedge goroutine is fired.

The system **SHOULD** expose a `carrier_p95_latency_ms{carrier_id=<id>}` Prometheus gauge that is updated each time the EMA tracker is updated.

#### Scenario: hedge_requests_total increments on hedge fire

- **GIVEN** the hedge monitor fires a hedge goroutine targeting CarrierAlpha
- **WHEN** `GET /metrics` is scraped after the request completes
- **THEN** the response contains `hedge_requests_total{carrier_id="alpha"} 1` (or higher if multiple hedges fired)

#### Scenario: carrier_p95_latency_ms reflects current EMA

- **GIVEN** CarrierAlpha's EMA p95 is `52ms` after 15 observations
- **WHEN** `GET /metrics` is scraped
- **THEN** the response contains `carrier_p95_latency_ms{carrier_id="alpha"} 52`

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority    | Scenarios |
|----------------|-------|-------------|-----------|
| REQ-HEDGE-001  | ADDED | MUST        | 3         |
| REQ-HEDGE-002  | ADDED | MUST        | 2         |
| REQ-HEDGE-003  | ADDED | MUST        | 3         |
| REQ-HEDGE-004  | ADDED | MUST        | 3         |
| REQ-HEDGE-005  | ADDED | MUST        | 2         |
| REQ-HEDGE-006  | ADDED | MUST/SHOULD | 2         |

**Total Requirements**: 6
**Total Scenarios**: 15
