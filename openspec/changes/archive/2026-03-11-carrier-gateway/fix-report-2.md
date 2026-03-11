# Fix Report: carrier-gateway — Iteration 2

**Source Gate**: review
**Date**: 2026-03-11
**Status**: SUCCESS
**Fixes Applied**: 3/3

## Fixes Applied

| # | File | Line | Category | Fix Applied |
|---|------|------|----------|-------------|
| W-01 | `internal/ratelimiter/limiter.go` | 60-66 | design-contract | Removed `l.metrics.RecordRateLimitRejection(l.carrierID)` from TryAcquire false branch. Simplified to `return l.inner.Allow()`. Updated doc comment to match design: "Does NOT emit a rejection metric on false — hedge suppression is silent." |
| S-01 | `internal/orchestrator/orchestrator.go` | 97-99 | observability | Added `o.log.Warn("no eligible carriers after capability filter", slog.String("request_id", req.RequestID), slog.Any("requested_lines", req.CoverageLines))` when `filterEligibleCarriers` returns empty slice. |
| S-02 | `internal/orchestrator/orchestrator.go` | 193-208 | efficiency | Added circuit breaker Open-state pre-filter in `filterEligibleCarriers`. Carriers with `breaker.State() == ports.CBStateOpen` are skipped before capability matching. HalfOpen carriers still pass (probe calls per REQ-CB-003). Existing CB check inside `callCarrier` goroutines remains as safety net. |

## Fixes Remaining

| # | File | Line | Category | Reason |
|---|------|------|----------|--------|
| — | — | — | — | None |

## Build Health After Fixes

| Check | Result |
|-------|--------|
| Typecheck (`go vet ./...`) | PASS |
| Lint (`golangci-lint run ./...`) | N/A (not installed) |
| Tests (`go test ./...`) | SKIPPED (Windows AppLocker policy) |
| Format (`gofmt -l .`) | PASS |
| Build (`go build ./...`) | PASS |
