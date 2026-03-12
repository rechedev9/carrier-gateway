# SDD Pipeline Results: db-close-on-shutdown

**Date:** 2026-03-12
**Bug:** H1 from fixes.md — `*sql.DB` connection never closed on graceful shutdown

---

## Pipeline Phases Executed

| Phase | Artifact | Status |
|-------|----------|--------|
| `/sdd:new` (explore) | `exploration.md` | DONE |
| `/sdd:new` (propose) | `proposal.md` | DONE |
| `/sdd:ff` (spec) | `specs/db-lifecycle/spec.md` | DONE |
| `/sdd:ff` (design) | `design.md` | DONE |
| `/sdd:ff` (tasks) | `tasks.md` | DONE |
| `/sdd:apply` | Code implementation + `apply-report.md` | DONE |
| `/sdd:close` | `log.md` updated, quality-timeline written | DONE |

---

## Fix Summary

### Problem
`repository.Open(dsn)` returns a `*sql.DB` that was declared inside a block-scoped `if` statement and never closed. PostgreSQL held connection-pool slots until the OS reclaimed them after process exit.

### Solution
1. **Hoisted `db` to function scope** — declared `var db *sql.DB` alongside `var repo` using a `var()` group.
2. **Assigned via `=` instead of `:=`** — `db, err = repository.Open(dsn)` (not `:=`) so the function-scoped variable is set.
3. **Added `db.Close()` in shutdown section** — after cleanup ticker stop and HTTP server drain, with nil guard and error logging.

### Shutdown Order (verified correct)
```
SIGTERM received
  → cleanupTicker.Stop()     // stops DeleteExpired calls
  → h.Shutdown(srv)          // drains in-flight HTTP requests
  → db.Close()               // releases connection pool  ← NEW
  → os.Exit(0)
```

---

## Files Changed

### Modified (1 file)
| File | Lines Changed | Description |
|------|---------------|-------------|
| `cmd/carrier-gateway/main.go` | ~12 | Added `database/sql` import, hoisted `db` var, added `db.Close()` block |

### SDD Artifacts Created (7 files)
| File | Purpose |
|------|---------|
| `openspec/changes/db-close-on-shutdown/exploration.md` | Codebase exploration |
| `openspec/changes/db-close-on-shutdown/proposal.md` | Change proposal |
| `openspec/changes/db-close-on-shutdown/specs/db-lifecycle/spec.md` | 3 requirements, 3 scenarios |
| `openspec/changes/db-close-on-shutdown/design.md` | Architecture + data flow |
| `openspec/changes/db-close-on-shutdown/tasks.md` | 1 task (completed) |
| `openspec/changes/db-close-on-shutdown/apply-report.md` | Implementation report |
| `openspec/changes/db-close-on-shutdown/quality-timeline.jsonl` | Phase tracking |

---

## Build Health

| Check | Result |
|-------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l .` | PASS (clean) |

---

## Session Metrics

| Metric | Count |
|--------|-------|
| **Total tool calls** | 22 |
| **Total files created** | 8 (0 code + 7 SDD artifacts + 1 results file) |
| **Total files modified** | 2 (main.go + log.md) |
| **Total conversation turns** | 10 |
| **Code LOC written** | ~12 (all in main.go — import, var group, db.Close block) |
