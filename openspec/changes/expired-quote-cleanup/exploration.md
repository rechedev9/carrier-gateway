# Exploration: expired-quote-cleanup

## Intent
The quotes table in PostgreSQL grows unbounded because `DeleteExpired` is never called. Implement a background cleanup ticker that periodically purges expired quotes.

## Relevant Files

| File | Role | Lines |
|------|------|-------|
| `internal/repository/postgres.go` | Has `DeleteExpired(ctx) (int64, error)` at L156 | 167 |
| `internal/ports/repository_port.go` | `QuoteRepository` interface — already declares `DeleteExpired` | 27 |
| `cmd/carrier-gateway/main.go` | Composition root — server start/shutdown, repo wiring (L54-72) | 264 |
| `internal/handler/handler.go` | Has `Shutdown()` method — graceful shutdown pattern reference | — |

## Key Observations

1. **`DeleteExpired` exists but is orphaned** — defined in both the interface and implementation, never called anywhere.
2. **Repo is optional** — `var repo ports.QuoteRepository` is nil when `DATABASE_URL` is unset. The ticker must be nil-safe.
3. **Shutdown pattern** — `main.go` uses `signal.NotifyContext` + `srv.ListenAndServe` in a goroutine + `h.Shutdown()`. The ticker must integrate into this existing shutdown flow.
4. **No existing background goroutine pattern** — this will be the first standalone background worker in the codebase.
5. **Hexagonal layering** — the ticker is infrastructure, not domain. It should live in a new package or in `repository/` alongside the repo it calls.

## Architectural Fit

The ticker is a **composition-root concern** — it's wired in `main.go`, receives the repo via DI, and is stopped during shutdown. The ticker logic itself should be a small, testable struct in its own package (e.g., `internal/cleanup`) to keep `main.go` lean and the ticker unit-testable.

## Blocking Questions
None — the approach is straightforward.
