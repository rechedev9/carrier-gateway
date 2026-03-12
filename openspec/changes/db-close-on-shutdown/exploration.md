# Exploration: db-close-on-shutdown

## Intent
The `*sql.DB` returned by `repository.Open(dsn)` is never closed on shutdown. PostgreSQL holds connection-pool slots until the OS reclaims them after process exit. Fix: close the DB connection during graceful shutdown.

## Relevant Files

| File | Role | Key Lines |
|------|------|-----------|
| `cmd/carrier-gateway/main.go` | Composition root — `repository.Open` at L57, `db` scoped inside `if/else` block (L56-72), shutdown at L208-216 | 290 |
| `internal/repository/postgres.go` | `Open(dsn) (*sql.DB, error)` at L43 — returns the `*sql.DB` handle | 167 |

## Key Observations

1. **`db` is block-scoped** — declared inside `if dsn := ...; dsn != "" { db, err := repository.Open(dsn) ... }`. A simple `defer db.Close()` inside that block would fire when the block exits (immediately), not at shutdown.
2. **Fix approach** — hoist `db` to function scope (`var db *sql.DB`), assign it inside the `if` block, then close it in the shutdown section (after HTTP server and cleanup ticker stop, before `os.Exit`).
3. **Shutdown order matters** — close DB *after* cleanup ticker stops (ticker calls `DeleteExpired` which uses the DB) and *after* HTTP server drains (handler may still be writing quotes).
4. **Nil-safety** — `db` will be nil when `DATABASE_URL` is unset or `Open` fails. Must guard with `if db != nil`.

## Blocking Questions
None.
