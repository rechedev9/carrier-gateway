# Project Log

Checkpoint file. Read this at the start of every session before touching any code.
Append an entry after every meaningful unit of work.

---

## Format

```
### YYYY-MM-DD — <short title>
**Status:** done | in-progress | blocked
**Files touched:** list of files
**What:** what was done
**Why:** why it was done
**Next:** what comes next (if known)
```

---

## Log

### 2026-03-11 — Initial MVP commit
**Status:** done
**Files touched:** all (initial commit `aacde5e`)
**What:** Built the full carrier-gateway MVP from scratch.
- Hexagonal architecture: `domain → ports → {circuitbreaker, ratelimiter, orchestrator, adapter, metrics, handler}`
- `internal/circuitbreaker` — 3-state CAS machine (Closed/Open/HalfOpen)
- `internal/ratelimiter` — per-carrier token bucket (`Wait` + `TryAcquire`)
- `internal/orchestrator` — fan-out via errgroup, adaptive hedge monitor with EMA p95 tracker
- `internal/adapter` — type-erased registry + MockCarriers (Alpha/Beta/Gamma)
- `internal/metrics` — isolated Prometheus registry, 8 metrics
- `internal/handler` — Go 1.22 ServeMux, `POST /quotes`, `GET /metrics`, graceful shutdown
- `cmd/carrier-gateway/main.go` — composition root
- PostgreSQL quote repository (`internal/repository/postgres.go`) + port interface
- HTTP carrier client (`internal/adapter/http_carrier.go`) + Delta carrier adapter
**Why:** Greenfield project.
**Next:** Bug fixes, then additional features.

---

### 2026-03-11 — Post-MVP bug fixes (4 bugs)
**Status:** done
**Files touched:**
- `internal/orchestrator/hedging.go`
- `internal/orchestrator/orchestrator.go`
- `internal/adapter/http_carrier.go`
- `go.mod`, `go.sum`
**What:**
1. **Bug 1 (behavioral)** — `hedgeMonitor` always used hard-coded `hedgePollInterval = 5ms` constant, ignoring `Config.HedgePollInterval`. Added `pollInterval time.Duration` parameter to `hedgeMonitor`; passed `o.cfg.HedgePollInterval` at call site; deleted dead constant.
2. **Bug 2 (panic)** — `ok` from `registry.Get` was silently dropped when building the `hedgeable` slice. A carrier with no registered adapter would produce a nil `execFn` that panicked when called in the hedge goroutine. Added `ok` guard mirroring `callCarrier`.
3. **Bug 3 (performance)** — `strings.NewReader(string(payload))` copied the already-marshalled `[]byte` payload to a `string` unnecessarily. Replaced with `bytes.NewReader(payload)`.
4. **Bug 4 (go.mod)** — `github.com/lib/pq` was annotated `// indirect` despite a direct blank import in `internal/repository/postgres.go`. Ran `go mod tidy` to fix.
**Why:** Found during post-MVP code review. See `bug.md` for full details.
**Next:** Commit all changes (bug fixes + log.md + bug.md).

---

### 2026-03-11 — System audit + fixes.md
**Status:** done
**Files touched:** `fixes.md` (created)
**What:** Full audit of all 29 .go files. Found 14 issues across 4 severity levels.
Documented each with exact fix in `fixes.md`. Key findings:
- C1: `/metrics` serves wrong Prometheus registry — carrier metrics never exposed
- H1: `*sql.DB` never closed on shutdown
- H2: `DeleteExpired` never called — table grows forever
- H3: Error logs use `X-Request-ID` header instead of body `request_id`
- H4: Zero `EMAAlpha` + zero `EMAWindowSize` silently freezes EMA tracker
- M1–M5, L1–L4: see fixes.md
**Why:** Proactive quality audit after MVP + bug fix phase.
**Next:** Implement fixes in priority order from fixes.md (FIX-C1 first).

---

### 2026-03-11 — Added log.md and local checkpoint rule
**Status:** done
**Files touched:** `log.md` (created), `CLAUDE.md` (rule added)
**What:** Created this checkpoint log and added a rule to `CLAUDE.md` requiring it to be read at session start and updated after each unit of work.
**Why:** Context compaction wipes in-memory state. The log gives the next session a reliable checkpoint without relying on git log or re-reading all files.
**Next:** —

---

### 2026-03-12 — Background cleanup ticker for expired quotes
**Status:** done
**Files touched:** `internal/cleanup/cleanup.go` (created), `cmd/carrier-gateway/main.go` (modified)
**What:** Implemented `cleanup.Ticker` — a background goroutine that calls `DeleteExpired` on the quote repository at a configurable interval (default 5m, override via `CLEANUP_INTERVAL` env var). Wired into `main.go`: starts after repo is ready (only when repo != nil), stops gracefully before server shutdown. Also created `ticker_test.go` (from prior session) that validates tick firing, clean stop, and context cancellation.
**Why:** `DeleteExpired` existed but was never called — the `quotes` table would grow unbounded (fixes.md H2).
**Next:** —

---

### 2026-03-12 — Close DB connection on shutdown
**Status:** done
**Files touched:** `cmd/carrier-gateway/main.go` (modified)
**What:** Hoisted `*sql.DB` to function scope and added `db.Close()` in the graceful shutdown section. Shutdown order: cleanup ticker → HTTP server drain → DB close. Nil-safe when no DB is configured. Fixes H1 from fixes.md.
**Why:** `repository.Open` returned a `*sql.DB` that was never closed — PostgreSQL connection-pool slots leaked until OS process cleanup.
**Next:** Continue implementing fixes from fixes.md.

---

### 2026-03-12 — Add ReadTimeout to HTTP server (FIX-M3)
**Status:** done
**Files touched:** `cmd/carrier-gateway/main.go`
**What:** Added `ReadTimeout: 15 * time.Second` to `http.Server` config. Prevents slowloris DoS where a client drip-feeds the request body indefinitely while holding a connection.
**Why:** `ReadHeaderTimeout` alone only caps header read time — body read was unbounded (fixes.md M3).
**Next:** Continue implementing fixes from fixes.md.

---

### 2026-03-12 — Validate EMA alpha in NewEMATracker (FIX-H4)
**Status:** done
**Files touched:** `internal/orchestrator/hedging.go`, `internal/orchestrator/hedging_test.go`
**What:** Added guard after alpha resolution in `NewEMATracker`: if `alpha <= 0 || alpha >= 1`, defaults to `0.1`. Added `TestEMATracker_ZeroAlphaDefaultsToPointOne` proving the tracker converges instead of freezing when both `EMAAlpha` and `EMAWindowSize` are zero.
**Why:** Zero alpha + zero window size silently froze the EMA tracker — p95 never moved from seed (fixes.md H4).
**Next:** Continue implementing fixes from fixes.md.

---

### 2026-03-12 — Batch audit fixes (FIX-C1, FIX-M1, FIX-H3, FIX-M2, FIX-M5, FIX-L1, FIX-L3, FIX-L4)
**Status:** done
**Files touched:**
- `internal/handler/http.go` — added `prometheus.Gatherer` field, `requestID` param threading
- `internal/handler/http_test.go` — updated for new `handler.New` signature, `io.Discard`
- `cmd/carrier-gateway/main.go` — pass `reg` to handler, explicit orchestrator config
- `internal/orchestrator/orchestrator.go` — hedgeMonitor into errgroup
- `internal/orchestrator/hedging.go` — `sync.WaitGroup` for hedge goroutines, sub-ms EMA fix
- `internal/orchestrator/orchestrator_test.go` — `io.Discard`
- `internal/adapter/mock_carrier.go` — negative latency clamp
- `internal/adapter/mock_carrier_test.go` — `io.Discard`
- `internal/adapter/http_carrier_test.go` — `io.Discard`
- `CLAUDE.md` — stale hedgeMonitor comment
**What:**
1. **FIX-C1** — `/metrics` now serves the isolated Prometheus registry via `promhttp.HandlerFor`
2. **FIX-M1** — Hedge goroutines tracked by `sync.WaitGroup`; `hedgeMonitor` runs inside errgroup
3. **FIX-H3** — Error logs use body `request_id` (falls back to `X-Request-ID` header pre-parse)
4. **FIX-M2** — EMA uses float64 division instead of int64 truncation
5. **FIX-M5** — MockCarrier clamps negative latency to 0
6. **FIX-L1** — All test loggers use `io.Discard`
7. **FIX-L3** — CLAUDE.md hedgeMonitor comment updated
8. **FIX-L4** — Explicit `HedgePollInterval: 5ms` in main.go
**Why:** Full audit pass. These were all remaining open items from fixes.md.
**Next:** Remaining new audit findings (AUD-H3 through AUD-M8) — HTTP client transport limits, timer leak in retry, DB pool tuning, health endpoint, TLS certs in Docker image.
