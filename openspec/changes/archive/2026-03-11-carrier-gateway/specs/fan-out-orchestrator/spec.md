# Delta Spec: Fan-Out Orchestrator

**Change**: carrier-gateway
**Date**: 2026-03-11T18:30:00Z
**Status**: draft
**Depends On**: proposal.md

---

## Context

The orchestrator is the central concurrency engine. It fans out one goroutine per eligible carrier, collects results via a shared buffered channel, and returns the aggregated slice of `QuoteResult` values to the HTTP handler. It also runs the hedge monitor goroutine (see adaptive-hedging spec). This spec covers the fan-out/fan-in behavior and result aggregation; hedging behavior is specified in the adaptive-hedging domain.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-ORCH-001: Carrier Eligibility Filtering

The orchestrator **MUST** filter the registered carrier list to only those whose `Carrier.Capabilities` intersect with `QuoteRequest.CoverageLines` before dispatching goroutines.

The orchestrator **MUST NOT** dispatch goroutines to carriers whose capabilities do not intersect the requested coverage lines, even if those carriers are healthy and under rate limit.

#### Scenario: Only capable carriers receive goroutines

- **GIVEN** three carriers are registered: Alpha (`Capabilities: ["auto","home"]`), Beta (`Capabilities: ["auto"]`), Gamma (`Capabilities: ["life"]`)
- **WHEN** the orchestrator receives `QuoteRequest{CoverageLines: ["auto"]}`
- **THEN** goroutines are dispatched to Alpha and Beta only; Gamma receives no goroutine and its adapter is never invoked

#### Scenario: Request with no matching carriers returns empty result

- **GIVEN** all registered carriers have `Capabilities: ["home"]`
- **WHEN** the orchestrator receives `QuoteRequest{CoverageLines: ["life"]}`
- **THEN** the orchestrator returns an empty `[]QuoteResult{}` with no error, and logs a `slog.Warn` line with `request_id` and the message "no eligible carriers for coverage lines"

---

### REQ-ORCH-002: Concurrent Fan-Out Dispatch

The orchestrator **MUST** dispatch one goroutine per eligible carrier concurrently. All carrier goroutines **MUST** start before the orchestrator begins blocking on result collection.

The orchestrator **MUST** use a buffered channel of size equal to the number of eligible carriers so that no goroutine blocks sending its result when the collector is busy processing another result.

#### Scenario: All goroutines start concurrently

- **GIVEN** three eligible carriers (Alpha, Beta, Gamma) and a `QuoteRequest` with 10s timeout
- **WHEN** the orchestrator fans out
- **THEN** all three carrier goroutines begin executing within 1ms of each other (verified in table-driven benchmark; not enforced at runtime beyond "all started before collect loop")

#### Scenario: Buffered channel prevents goroutine leak

- **GIVEN** two eligible carriers (Alpha, Beta) and a results channel of size 2
- **WHEN** both goroutines complete before the collector reads the first result
- **THEN** both goroutines exit cleanly without blocking, and the collector subsequently drains both results; `goleak` reports zero leaked goroutines after the call returns

---

### REQ-ORCH-003: Context-Scoped Timeout

The orchestrator **MUST** derive a child `context.Context` from the request context using `context.WithTimeout`, where the timeout equals `QuoteRequest.Timeout` if non-zero, or the orchestrator's configured default timeout otherwise.

The orchestrator **MUST** cancel all in-flight goroutines when the derived context expires or the parent context is cancelled.

#### Scenario: Per-carrier goroutine respects context cancellation

- **GIVEN** CarrierGamma is configured to sleep 800ms and the request timeout is 500ms
- **WHEN** the orchestrator's context expires at 500ms
- **THEN** CarrierGamma's goroutine receives `context.Canceled` or `context.DeadlineExceeded` from the mock's `select` on `ctx.Done()`, logs the cancellation, and exits — it does not send a result to the channel

#### Scenario: Short-timeout request returns only fast results

- **GIVEN** Alpha responds in ~50ms, Beta in ~200ms, Gamma in ~800ms, and timeout is 300ms
- **WHEN** the orchestrator fans out and the 300ms deadline is reached
- **THEN** the returned `[]QuoteResult` contains Alpha's result (and possibly Beta's if it completed in time) but NOT Gamma's result; no goroutine leak occurs

---

### REQ-ORCH-004: Result Collection and Aggregation

The orchestrator **MUST** collect results from the shared channel until either (a) all eligible carriers (including any hedge goroutines) have responded, (b) the context deadline is reached, or (c) all pending goroutines have exited.

The orchestrator **MUST** deduplicate results: if both a primary goroutine and a hedge goroutine return a result for the same `CarrierID`, only the first arrival **MUST** be included in the output.

The orchestrator **SHOULD** sort the final `[]QuoteResult` by `Premium` ascending before returning.

#### Scenario: Duplicate carrier results are deduplicated

- **GIVEN** a primary goroutine for CarrierBeta and a hedge goroutine for CarrierBeta both send results to the channel
- **WHEN** the collector processes both
- **THEN** the returned slice contains exactly one `QuoteResult` with `CarrierID == "beta"` — the one that arrived first; the duplicate is discarded and logged at `slog.Debug` level

#### Scenario: Results sorted by Premium ascending

- **GIVEN** CarrierAlpha returns `Premium: 200.00` and CarrierBeta returns `Premium: 150.00`
- **WHEN** the orchestrator completes collection
- **THEN** the returned slice is `[{CarrierID: "beta", Premium: 150.00, ...}, {CarrierID: "alpha", Premium: 200.00, ...}]`

#### Scenario: Partial results returned on timeout

- **GIVEN** two carriers are eligible, Alpha responds in 50ms, Gamma has not responded when the 300ms deadline fires
- **WHEN** the orchestrator returns
- **THEN** the result slice contains exactly Alpha's result; no error is returned to the caller; the function return signature is `([]QuoteResult, error)` and error is `nil`

---

### REQ-ORCH-005: Goroutine Leak Prevention

The orchestrator **MUST** guarantee that no goroutines it spawns outlive the enclosing function call.

The orchestrator **MUST** use `context.WithTimeout` or `context.WithCancel` (via `golang.org/x/sync/errgroup`) and ensure all goroutines select on `ctx.Done()`.

The orchestrator **MUST NOT** use `sync.WaitGroup` as the sole mechanism for draining goroutines, since a goroutine blocked on a carrier call with no context check will not exit until the call unblocks.

#### Scenario: No goroutine leak after context cancellation (race detector)

- **GIVEN** an orchestrator fan-out of three carriers, one of which never responds (simulated by a goroutine that blocks indefinitely unless ctx is cancelled)
- **WHEN** the test cancels the context after 100ms and `goleak.VerifyNone(t)` is called
- **THEN** `goleak.VerifyNone` reports zero leaked goroutines

---

### REQ-ORCH-006: Structured Logging per Goroutine

Every log line emitted inside a carrier goroutine **MUST** carry `carrier_id` and `request_id` as structured `slog` fields.

The orchestrator **MUST NOT** use `fmt.Println`, `log.Printf`, or any unstructured logging call.

#### Scenario: Log line carries required fields

- **GIVEN** a fan-out request with `request_id == "req-xyz"` and CarrierAlpha is dispatched
- **WHEN** CarrierAlpha's goroutine completes successfully
- **THEN** the `slog` record contains `"carrier_id": "alpha"` and `"request_id": "req-xyz"` as key-value pairs at `slog.Debug` or `slog.Info` level

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority | Scenarios |
|----------------|-------|----------|-----------|
| REQ-ORCH-001   | ADDED | MUST     | 2         |
| REQ-ORCH-002   | ADDED | MUST     | 2         |
| REQ-ORCH-003   | ADDED | MUST     | 2         |
| REQ-ORCH-004   | ADDED | MUST/SHOULD | 3      |
| REQ-ORCH-005   | ADDED | MUST     | 1         |
| REQ-ORCH-006   | ADDED | MUST     | 1         |

**Total Requirements**: 6
**Total Scenarios**: 11
