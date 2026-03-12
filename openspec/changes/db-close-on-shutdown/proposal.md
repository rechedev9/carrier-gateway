# Proposal: db-close-on-shutdown

## Intent
Close the `*sql.DB` connection during graceful shutdown to release PostgreSQL connection-pool slots immediately instead of leaking them until OS process cleanup.

## Scope

### In Scope
- Hoist `db` variable to function scope in `main.go`
- Close DB after cleanup ticker and HTTP server have stopped
- Log the close result

### Out of Scope
- Connection pool tuning (`SetMaxOpenConns`, etc.) — separate concern
- Health check endpoint for DB liveness

## Approach
1. Declare `var db *sql.DB` at function scope in `main()`, before the `if dsn` block.
2. Assign `db` inside the success path of `repository.Open`.
3. In the graceful shutdown section, after cleanup ticker stop and HTTP server shutdown, call `db.Close()` with a nil guard.
4. Log any close error at warn level (non-fatal — process is exiting anyway).

## Affected Areas
- `cmd/carrier-gateway/main.go` — single file modification (~8 lines changed)

## Risks
| Risk | Mitigation |
|------|------------|
| Closing DB before in-flight queries finish | Shutdown order: HTTP drain → cleanup ticker stop → DB close |
| `db.Close()` called on nil | Explicit `if db != nil` guard |

## Rollback Plan
Revert the ~8-line diff in `main.go`.

## Dependencies
None.

## Success Criteria
- `db.Close()` is called during graceful shutdown when a DB connection exists.
- Shutdown order is: cleanup ticker → HTTP server → DB close.
- `go build ./...` and `go vet ./...` pass.
