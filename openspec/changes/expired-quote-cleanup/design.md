# Design: expired-quote-cleanup

## Technical Approach

Create a minimal `internal/cleanup` package containing a `Ticker` struct that wraps `time.Ticker` and calls `DeleteExpired` on each tick in a background goroutine.

## Architecture Decisions

| Decision | Rationale |
|----------|-----------|
| Separate `internal/cleanup` package | Keeps ticker testable in isolation; avoids bloating `repository/` or `main.go` |
| Accept `ports.QuoteRepository` interface | Follows hexagonal pattern — ticker depends on port, not concrete repo |
| `done` channel for stop signal | Idiomatic Go pattern; simpler than context cancellation for long-lived workers |
| Log-and-continue on errors | Transient DB errors should not kill the cleanup loop |

## Data Flow

```
main.go
  └─ cleanup.New(repo, interval, logger)
       └─ Start() → goroutine
            └─ ticker.C → repo.DeleteExpired(ctx) → log result
       └─ Stop() → close(done) → goroutine exits
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/cleanup/ticker.go` | CREATE | Ticker struct with New/Start/Stop |
| `internal/cleanup/ticker_test.go` | CREATE | Unit test with mock repo |
| `cmd/carrier-gateway/main.go` | MODIFY | Wire ticker start/stop, read CLEANUP_INTERVAL env |

## Interfaces

```go
// Ticker manages periodic cleanup of expired quotes.
type Ticker struct {
    repo     ports.QuoteRepository
    interval time.Duration
    log      *slog.Logger
    done     chan struct{}
    stopped  chan struct{}
}

func New(repo ports.QuoteRepository, interval time.Duration, log *slog.Logger) *Ticker
func (t *Ticker) Start()
func (t *Ticker) Stop()
```

## Testing Strategy

- **Unit test**: Inject a stub repository that counts `DeleteExpired` calls. Start ticker with short interval (50ms), wait, stop, assert call count > 0 and no goroutine leaks.
- **Build verification**: `go build ./...` and `go vet ./...`
