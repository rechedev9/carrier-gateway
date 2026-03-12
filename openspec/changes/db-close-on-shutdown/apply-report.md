# Apply Report: db-close-on-shutdown

## Summary
Closed the `*sql.DB` connection during graceful shutdown to prevent PostgreSQL connection-pool slot leaks.

## Files Modified
| File | Change |
|------|--------|
| `cmd/carrier-gateway/main.go` | Added `database/sql` import, hoisted `db` to function scope, added `db.Close()` in shutdown section |

## Build Health
| Check | Status |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | PASS |

## Tasks Completed
- [x] 1.1 — Hoist `db` + add `db.Close()` in shutdown

## Spec Coverage
| Requirement | Covered By |
|-------------|------------|
| REQ-1: DB Closed on Shutdown | `db.Close()` at L224-230 |
| REQ-2: Nil-Safe When No DB | `if db != nil` guard at L224 |
| REQ-3: Correct Shutdown Order | ticker stop (L214) → HTTP drain (L218) → DB close (L224) |
