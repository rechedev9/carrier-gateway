# Proposal: expired-quote-cleanup

## Intent
Implement a background cleanup ticker that periodically calls `DeleteExpired` on the PostgreSQL repository to prevent unbounded table growth.

## Scope

### In Scope
- New `internal/cleanup` package with a `Ticker` struct
- Configurable interval via environment variable `CLEANUP_INTERVAL` (default: 5 minutes)
- Graceful start/stop integrated into `main.go` shutdown flow
- Structured logging of each cleanup cycle (rows deleted)
- Unit test for the ticker

### Out of Scope
- Metrics for cleanup operations (can be added later)
- Batch-size limits on DELETE (current volume doesn't warrant it)
- Admin API to trigger cleanup manually

## Approach
1. Create `internal/cleanup/ticker.go` — a `Ticker` struct wrapping `time.Ticker`, the repo interface, a logger, and a stop channel.
2. `Start()` launches a goroutine that calls `repo.DeleteExpired()` on each tick.
3. `Stop()` signals the goroutine to exit and waits for it to drain.
4. In `main.go`, instantiate and start the ticker after repo is confirmed non-nil; stop it before server shutdown.

## Affected Areas
- `internal/cleanup/` — **new package** (ticker.go, ticker_test.go)
- `cmd/carrier-gateway/main.go` — wire ticker start/stop

## Risks
| Risk | Mitigation |
|------|------------|
| Ticker runs when repo is nil | Guard in main.go — only start ticker when repo != nil |
| DELETE under load causes lock contention | `expires_at` index already exists; DELETE hits only expired rows |

## Rollback Plan
Remove the `internal/cleanup/` package and revert the 3-line wiring in `main.go`.

## Dependencies
None — uses only stdlib + existing `ports.QuoteRepository` interface.

## Success Criteria
- `DeleteExpired` is called on a configurable interval while the server is running.
- Ticker stops cleanly on SIGTERM/SIGINT with no goroutine leaks.
- `go build ./...` and `go vet ./...` pass.
