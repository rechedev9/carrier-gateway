# Fix Report — Iteration 1

**Change:** carrier-gateway
**Source Gate:** review
**Iteration:** 1
**Date:** 2026-03-11

## Summary

Applied all 8 CRITICAL issues and 4 WARNING fixes from the review report. All fixes are mechanical renames, constant swaps, metric name corrections, log-level adjustments, and a new `EMAWindowSize` config field. Three test files required updates to match the new contracts.

## Issues Fixed

### CRITICAL (8/8)

| Issue | File(s) | Description |
|-------|---------|-------------|
| ISSUE-01 | `ports/metrics_port.go`, `circuitbreaker/breaker.go`, `metrics/prometheus.go` | Swapped CBStateOpen=1 and CBStateHalfOpen=2 to match standard CB state ordering |
| ISSUE-02 | `domain/errors.go`, `handler/http.go`, `orchestrator/orchestrator.go` | Renamed `ErrRequestTimeout` to `ErrCarrierTimeout` |
| ISSUE-03 | `domain/errors.go`, `ratelimiter/limiter.go`, `ratelimiter/limiter_test.go`, `orchestrator/orchestrator.go` | Renamed `ErrRateLimited` to `ErrRateLimitExceeded` |
| ISSUE-04 | `domain/errors.go`, `handler/http.go` | Added `ErrInvalidRequest` sentinel; all validation errors wrap it via `fmt.Errorf("%w: ...")` |
| ISSUE-05 | `metrics/prometheus.go` | Renamed metric `rate_limit_rejections_total` to `rate_limit_exceeded_total` |
| ISSUE-06 | `orchestrator/orchestrator.go`, `handler/http.go`, `orchestrator/orchestrator_test.go` | No-eligible-carriers returns `([], nil)` from orchestrator; handler checks empty results and returns 422 |
| ISSUE-07 | `adapter/adapter.go`, `adapter/adapter_test.go`, `orchestrator/orchestrator_test.go`, `cmd/carrier-gateway/main.go` | Renamed `Registry.Add()` to `Registry.Register()` across all call sites |
| ISSUE-08 | `domain/carrier.go`, `orchestrator/hedging.go`, `cmd/carrier-gateway/main.go` | Added `EMAWindowSize int` field; `NewEMATracker` derives `alpha = 2/(N+1)` when `EMAWindowSize > 0`; all 3 carriers set `EMAWindowSize: 19` |

### WARNING (4/4)

| Warning | File | Description |
|---------|------|-------------|
| WARN-01 | `orchestrator/hedging.go` | Added `slog.Warn("no eligible hedge candidate", ...)` when `selectHedgeCandidate` returns nil |
| WARN-02 | `orchestrator/orchestrator.go` | Changed duplicate-result log from `Info` to `Debug` |
| WARN-03 | `ratelimiter/limiter.go` | Added `RecordRateLimitRejection` call in `TryAcquire()` false branch |
| WARN-04 | `handler/http.go` | `writeError` now logs 5xx at Error level, 504 at Warn level, others at Info |

## Test Fixes

Three test files required updates to align with the new contracts:

- **`metrics/prometheus_test.go`**: Updated metric name from `rate_limit_rejections_total` to `rate_limit_exceeded_total`; swapped CB state expected values (Open=1, HalfOpen=2)
- **`handler/http_test.go`**: Table-driven "valid request" test case now provides a non-empty mock result (empty results now return 422 per ISSUE-06)
- **`orchestrator/orchestrator_test.go`**: Updated `NoMatchingCarriers` test to expect `nil` error + empty results; renamed all `.Add()` calls to `.Register()`

## Build Health

| Check | Result |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -w .` | PASS (no changes) |
| `go test ./...` | PASS (all packages) |
