# Apply Report: expired-quote-cleanup

## Summary
Implemented a background cleanup ticker that periodically calls `DeleteExpired` on the PostgreSQL repository to prevent unbounded table growth.

## Files Created
| File | Lines | Purpose |
|------|-------|---------|
| `internal/cleanup/cleanup.go` | 73 | Ticker struct — New/Start/Stop, sweep loop |
| `internal/cleanup/ticker_test.go` | 86 | Unit tests — call counting, clean stop, context cancellation |

## Files Modified
| File | Change |
|------|--------|
| `cmd/carrier-gateway/main.go` | Added cleanup ticker wiring: import, env var parsing, start/stop |

## Build Health
| Check | Status |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | PASS |

## Tasks Completed
- [x] 1.1 — Ticker struct (cleanup.go)
- [x] 2.1 — main.go wiring
- [x] 3.1 — Unit tests

## Spec Coverage
| Requirement | Covered By |
|-------------|------------|
| REQ-1: Periodic Cleanup | cleanup.go:Start/sweep, TestTicker_CallsDeleteExpired |
| REQ-2: Configurable Interval | main.go CLEANUP_INTERVAL env var parsing |
| REQ-3: Graceful Stop | cleanup.go:Stop, TestTicker_StopIsClean |
| REQ-4: Nil-Safety | main.go `if repo != nil` guard |
| REQ-5: Error Resilience | cleanup.go:sweep logs error and returns (no crash) |
