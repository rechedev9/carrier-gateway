# Design: db-close-on-shutdown

## Technical Approach

Hoist the `*sql.DB` variable to `main()` function scope and close it explicitly in the shutdown section after all DB-dependent components have stopped.

## Architecture Decisions

| Decision | Rationale |
|----------|-----------|
| Hoist `db` to function scope | Block-scoped `db` can't be referenced in shutdown section |
| Close after HTTP drain + ticker stop | Prevents closing the DB while queries are still in flight |
| Log-and-continue on close error | Process is exiting — a close error is informational, not fatal |

## Data Flow

```
Shutdown signal received
  → cleanupTicker.Stop()     (stops DeleteExpired calls)
  → h.Shutdown(srv)          (drains in-flight HTTP requests)
  → db.Close()               (releases connection pool)
  → os.Exit(0)
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `cmd/carrier-gateway/main.go` | MODIFY | Hoist `db` variable, add `db.Close()` in shutdown section |

## Interfaces
No new interfaces — this is a wiring-only change in the composition root.

## Testing Strategy
- **Build verification**: `go build ./...` and `go vet ./...`
- **Manual verification**: The change is composition-root wiring with no testable logic beyond "close is called". The nil guard and shutdown order are verified by code review.
