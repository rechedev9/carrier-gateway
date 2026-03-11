# Delta Spec: Rate Limiter

**Change**: carrier-gateway
**Date**: 2026-03-11T18:30:00Z
**Status**: draft
**Depends On**: proposal.md

---

## Context

The rate limiter provides per-carrier token-bucket rate limiting using `golang.org/x/time/rate`. Each carrier has its own `rate.Limiter` instance configured from `Carrier.RateLimit` (requests per second). The limiter is checked before dispatching a goroutine for a carrier, and if the bucket is exhausted, the carrier call is rejected with `ErrRateLimitExceeded`. Prometheus instrumentation tracks rate-limit rejections.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-RL-001: Per-Carrier Token-Bucket Limiter

The `ratelimiter` package **MUST** create one `rate.Limiter` instance per carrier, keyed by `Carrier.ID`. The limiter **MUST** be configured with rate = `Carrier.RateLimit` tokens per second and a burst equal to `Carrier.RateLimit` (burst == rate for this demo; a single burst cap is sufficient).

The package **MUST** expose a `LimiterRegistry` that stores per-carrier limiters and provides a `Allow(carrierID string) bool` method.

The `LimiterRegistry` **MUST NOT** share state between carriers — exhausting CarrierAlpha's bucket **MUST NOT** affect CarrierBeta's bucket.

#### Scenario: Limiter allows requests under rate cap

- **GIVEN** CarrierAlpha is configured with `RateLimit: 10` (10 RPS)
- **WHEN** 10 sequential calls to `registry.Allow("alpha")` are made within 1 second
- **THEN** all 10 calls return `true`

#### Scenario: Limiter rejects requests over rate cap

- **GIVEN** CarrierAlpha is configured with `RateLimit: 10` and 10 tokens have already been consumed in the current second
- **WHEN** an 11th call to `registry.Allow("alpha")` is made in the same second
- **THEN** `registry.Allow("alpha")` returns `false`

#### Scenario: Per-carrier isolation

- **GIVEN** CarrierAlpha's bucket is exhausted (0 tokens remaining) and CarrierBeta has a full bucket
- **WHEN** `registry.Allow("beta")` is called
- **THEN** it returns `true` — Beta's bucket is unaffected by Alpha's exhaustion

---

### REQ-RL-002: Rate-Limit Rejection Behavior

The orchestrator **MUST** call `registry.Allow(carrierID)` before dispatching a goroutine for each carrier. If `Allow` returns `false`, the orchestrator **MUST** skip that carrier for this request and **MUST NOT** invoke the carrier adapter.

The orchestrator **SHOULD** log a `slog.Warn` line with `carrier_id`, `request_id`, and message `"rate limit exceeded, skipping carrier"` when a carrier is skipped.

#### Scenario: Rate-limited carrier is skipped in fan-out

- **GIVEN** CarrierBeta's rate limiter bucket is exhausted
- **WHEN** the orchestrator fans out a `QuoteRequest` that includes Beta as an eligible carrier
- **THEN** no goroutine is dispatched for Beta; `QuoteResult` slice does not contain a Beta result; Beta's adapter `Adapt` method is never called

#### Scenario: Rate-limit warning logged

- **GIVEN** CarrierBeta's bucket is exhausted and a request is dispatched
- **WHEN** the orchestrator checks `registry.Allow("beta")` and gets `false`
- **THEN** a `slog.Warn` record is emitted containing `"carrier_id": "beta"` and `"request_id"` matching the current request

---

### REQ-RL-003: Prometheus Counter for Rate-Limit Events

The system **MUST** expose a `rate_limit_exceeded_total{carrier_id=<id>}` Prometheus counter that increments each time a carrier is skipped due to rate limiting.

#### Scenario: Counter increments on rate-limit rejection

- **GIVEN** CarrierBeta's bucket is exhausted and a request is rejected
- **WHEN** `GET /metrics` is scraped
- **THEN** the response contains `rate_limit_exceeded_total{carrier_id="beta"} 1` (or higher for multiple rejections)

#### Scenario: Counter is initialized to 0 at startup

- **GIVEN** the server has just started and no requests have been processed
- **WHEN** `GET /metrics` is scraped
- **THEN** `rate_limit_exceeded_total` counters are either absent or `0` for all carriers (Prometheus omits zero-value counters by default unless pre-registered with initial value)

---

### REQ-RL-004: Token Replenishment

The token-bucket **MUST** replenish tokens at the configured rate over time (this is handled automatically by `golang.org/x/time/rate`).

After token replenishment, previously-rejected carriers **MUST** become eligible for fan-out again without any manual reset.

#### Scenario: Carrier recovers from rate limiting after replenishment window

- **GIVEN** CarrierAlpha was rate-limited (bucket exhausted) at time T
- **WHEN** a new request arrives at time T + (1/RateLimit) seconds (one token has been replenished)
- **THEN** `registry.Allow("alpha")` returns `true` and the carrier is included in the fan-out

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority    | Scenarios |
|----------------|-------|-------------|-----------|
| REQ-RL-001     | ADDED | MUST        | 3         |
| REQ-RL-002     | ADDED | MUST/SHOULD | 2         |
| REQ-RL-003     | ADDED | MUST        | 2         |
| REQ-RL-004     | ADDED | MUST        | 1         |

**Total Requirements**: 4
**Total Scenarios**: 8
