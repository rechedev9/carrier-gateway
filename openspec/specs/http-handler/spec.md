# Spec: HTTP Handler

<!-- Added: 2026-03-11 from change: carrier-gateway -->

**Source Change**: carrier-gateway
**Merged**: 2026-03-11
**Status**: merged

---

## Context

The HTTP handler is the inbound port — the only entry point for external callers. It exposes two endpoints: `POST /quotes` (invokes the orchestrator) and `GET /metrics` (Prometheus scrape endpoint). It validates input, enforces request body size limits, translates domain errors to HTTP status codes, serializes responses as JSON, and handles graceful shutdown on SIGTERM/SIGINT.

All requirements are ADDED (greenfield project; no baseline specs exist).

---

## ADDED Requirements

### REQ-HTTP-001: POST /quotes Endpoint

The handler **MUST** expose `POST /quotes` that accepts a JSON body matching:

```json
{
  "request_id": "<non-empty string>",
  "coverage_lines": ["<non-empty string>", ...],
  "timeout_ms": <optional positive integer>
}
```

The handler **MUST** respond with HTTP 200 and a JSON array of `QuoteResult` objects on success, even if the array is empty.

The handler **MUST** apply `http.MaxBytesReader` with a limit of 1 MB to all request bodies.

#### Scenario: Valid request returns 200 with results

- **GIVEN** the server is running and all three carriers are healthy
- **WHEN** `POST /quotes` is called with body `{"request_id":"demo-1","coverage_lines":["auto"]}`
- **THEN** the response has HTTP status `200`, `Content-Type: application/json`, and body is a JSON array containing at least 1 `QuoteResult` object with fields `carrier_id`, `premium`, `coverage_lines`, `latency_ms`, and `hedged`

#### Scenario: Request body exceeding 1 MB is rejected

- **GIVEN** a client sends a request body of 1.5 MB
- **WHEN** the handler reads the body with `http.MaxBytesReader`
- **THEN** the response is HTTP 400 with JSON body `{"error":"REQUEST_TOO_LARGE","message":"request body exceeds 1 MB limit"}`

---

### REQ-HTTP-002: Input Validation

The handler **MUST** validate the parsed `QuoteRequest` before passing it to the orchestrator:

- `request_id` **MUST NOT** be empty
- `coverage_lines` **MUST NOT** be empty and **MUST NOT** contain empty strings
- `timeout_ms`, if present, **MUST** be a positive integer

The handler **MUST** return HTTP 400 with a structured JSON error body for all validation failures.

#### Scenario: Empty request_id rejected

- **GIVEN** body `{"request_id":"","coverage_lines":["auto"]}`
- **WHEN** `POST /quotes` is called
- **THEN** HTTP 400 with body `{"error":"INVALID_REQUEST","message":"request_id must not be empty"}`

#### Scenario: Missing coverage_lines rejected

- **GIVEN** body `{"request_id":"r1"}`
- **WHEN** `POST /quotes` is called
- **THEN** HTTP 400 with body `{"error":"INVALID_REQUEST","message":"coverage_lines must not be empty"}`

#### Scenario: Negative timeout_ms rejected

- **GIVEN** body `{"request_id":"r1","coverage_lines":["auto"],"timeout_ms":-500}`
- **WHEN** `POST /quotes` is called
- **THEN** HTTP 400 with body `{"error":"INVALID_REQUEST","message":"timeout_ms must be positive"}`

---

### REQ-HTTP-003: Domain Error to HTTP Status Mapping

The handler **MUST** map domain errors from the orchestrator to HTTP status codes as follows:

| Domain Error | HTTP Status |
|---|---|
| `ErrInvalidRequest` | 400 |
| `ErrRateLimitExceeded` (all carriers) | 429 |
| Any orchestrator timeout (partial results) | 200 (partial results returned, not an error) |
| Unexpected internal error | 500 |

The handler **MUST NOT** expose internal error messages or stack traces in HTTP 500 responses — the response body **MUST** be `{"error":"INTERNAL_ERROR","message":"an internal error occurred"}`.

#### Scenario: Internal error returns 500 without stack trace

- **GIVEN** the orchestrator returns an unexpected `errors.New("nil pointer in adapter")` error
- **WHEN** the handler processes the response
- **THEN** HTTP 500 with body `{"error":"INTERNAL_ERROR","message":"an internal error occurred"}` — no stack trace or internal detail in the response body; the full error is logged with `slog.Error`

---

### REQ-HTTP-004: GET /metrics Endpoint

The handler **MUST** expose `GET /metrics` using the default Prometheus HTTP handler (`promhttp.Handler()`).

The endpoint **MUST** return HTTP 200 with `Content-Type: text/plain; version=0.0.4` and Prometheus text exposition format.

#### Scenario: Metrics endpoint returns expected counters

- **GIVEN** at least one `POST /quotes` request has been processed
- **WHEN** `GET /metrics` is called
- **THEN** the response body contains all of: `carrier_circuit_breaker_state`, `carrier_p95_latency_ms`, `carrier_requests_total`, `hedge_requests_total`, and `carrier_quote_latency_ms_bucket` (histogram bucket lines)

---

### REQ-HTTP-005: Graceful Shutdown

The server **MUST** listen for `SIGTERM` and `SIGINT` signals and initiate graceful shutdown by calling `http.Server.Shutdown(ctx)` with a 30-second drain window.

During shutdown, in-flight requests **MUST** be allowed to complete. New connections **MUST** be rejected after the signal is received.

The process **MUST** exit with code 0 after successful drain, or code 1 if the drain window expires.

#### Scenario: SIGTERM triggers graceful shutdown

- **GIVEN** the server has an in-flight `POST /quotes` request that will complete in 200ms
- **WHEN** `SIGTERM` is sent to the process
- **THEN** the in-flight request completes and receives a valid response; the server exits with code 0 within the 30-second drain window

#### Scenario: Force exit after drain timeout

- **GIVEN** the server receives `SIGTERM` and a request is stuck for 35 seconds
- **WHEN** the 30-second shutdown drain window expires
- **THEN** the server calls `os.Exit(1)` and logs `slog.Error("shutdown drain timeout exceeded")`

---

### REQ-HTTP-006: Request Tracing via request_id

Every `slog` log line emitted during a `POST /quotes` request lifecycle **MUST** carry the `request_id` from the parsed `QuoteRequest` as a structured field.

The handler **MUST** propagate `request_id` into the context (or pass it explicitly to the orchestrator) so all downstream components can include it in their log lines.

#### Scenario: All log lines for a request carry request_id

- **GIVEN** a `POST /quotes` request with `request_id: "req-trace-99"`
- **WHEN** the request is processed end-to-end (handler → orchestrator → adapters)
- **THEN** every `slog` record emitted during this request (in handler, orchestrator, circuit breaker callbacks, adapters) contains `"request_id": "req-trace-99"`

---

## Acceptance Criteria Summary

| Requirement ID | Type  | Priority | Scenarios |
|----------------|-------|----------|-----------|
| REQ-HTTP-001   | ADDED | MUST     | 2         |
| REQ-HTTP-002   | ADDED | MUST     | 3         |
| REQ-HTTP-003   | ADDED | MUST     | 1         |
| REQ-HTTP-004   | ADDED | MUST     | 1         |
| REQ-HTTP-005   | ADDED | MUST     | 2         |
| REQ-HTTP-006   | ADDED | MUST     | 1         |

**Total Requirements**: 6
**Total Scenarios**: 10
