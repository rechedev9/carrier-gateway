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
**Next:** Remaining new audit findings (AUD-H3 through AUD-M8).

---

### 2026-03-12 — Audit fixes round 2 (AUD-H3, AUD-H4, AUD-H5, AUD-H6, AUD-M3, AUD-M5, AUD-M8)
**Status:** done
**Files touched:**
- `internal/adapter/http_carrier.go` — transport limits, timer leak fix
- `internal/repository/postgres.go` — connection pool tuning
- `internal/orchestrator/orchestrator.go` — detached context for repo.Save
- `internal/orchestrator/hedging.go` — sub-ms elapsed precision
- `internal/handler/http.go` — `/healthz` endpoint
- `cmd/carrier-gateway/main.go` — ADDR env var support
- `Dockerfile` — distroless/static:nonroot
**What:**
1. **AUD-H3** — HTTPCarrier transport: MaxIdleConns=50, MaxIdleConnsPerHost=10, IdleConnTimeout=90s
2. **AUD-H4** — Retry backoff uses time.NewTimer + Stop() instead of time.After
3. **AUD-H5** — GET /healthz returns 200 "ok" for liveness probes
4. **AUD-H6** — Docker runtime switched to distroless/static:nonroot (includes TLS root certs)
5. **AUD-M3** — PostgreSQL pool: MaxOpenConns=25, MaxIdleConns=5, ConnMaxLifetime=5m
6. **AUD-M5** — ADDR env var now used as default for -addr flag
7. **AUD-M8** — repo.Save uses detached 3s context instead of potentially-cancelled parent
**Why:** Production hardening from full codebase audit.
**Next:** FIX-L2, FIX-M4, and remaining cleanup.

---

### 2026-03-12 — Final audit cleanup (FIX-L2, FIX-M4, AUD-M4, AUD-M6)
**Status:** done
**Files touched:**
- `internal/domain/quote.go` — added `CarrierRef`, removed dead `Coverage` struct
- `internal/adapter/delta_carrier.go` — populates `CarrierRef` from `QuoteID`
- `internal/handler/http.go` — surfaces `carrier_ref` in JSON response
- `internal/orchestrator/orchestrator_test.go` — `TestOrchestrator_MissingRegistryEntry_NoPanic`
- `internal/adapter/adapter_test.go` — consistent `io.Discard` loggers
- `db/migrations/001_create_quotes.sql` — idempotent `IF NOT EXISTS`
**What:**
1. **FIX-L2** — `CarrierRef` field in `QuoteResult`, populated by Delta adapter, surfaced in JSON response
2. **FIX-M4** — Test for nil-exec guard: carrier in carriers slice but missing from registry is skipped without panic
3. **AUD-M6** — Removed dead `Coverage` struct and `Coverages` field (never populated by any adapter)
4. **AUD-M4** — Migration file now idempotent (`IF NOT EXISTS`) to match inline DDL
**Why:** Close out all remaining audit items.
**Next:** All fixes.md and audit findings are resolved. System is production-ready.

---

### 2026-03-12 — Split orchestrator_test.go (file size limit)
**Status:** done
**Files touched:**
- `internal/orchestrator/orchestrator_test.go` — trimmed to helpers + unit tests (247 lines)
- `internal/orchestrator/orchestrator_integration_test.go` — new file with integration tests, benchmark (347 lines)
**What:** Split 581-line `orchestrator_test.go` into two files to comply with the 500-line hard limit. Same package (`orchestrator_test`) so helpers are shared without duplication. Unit tests (Part 1) stay in original; integration/concurrency tests (Part 2), benchmark, and `MissingRegistryEntry_NoPanic` moved to new file.
**Why:** CLAUDE.md file organization rule: 500 lines = error.
**Next:** —

---

### 2026-03-12 — Rename orchestrator integration test + add e2e test
**Status:** done
**Files touched:**
- `internal/orchestrator/orchestrator_integration_test.go` → `internal/orchestrator/orchestrator_concurrency_test.go` (renamed, header updated)
- `cmd/carrier-gateway/e2e_test.go` (new, 260 lines)
**What:** Renamed the orchestrator "integration" test to "concurrency" (accurate naming — these are mock-carrier concurrency tests, not real I/O). Added a true e2e test that boots the full composition root via `httptest.NewServer` and exercises real HTTP endpoints: healthz, happy-path quotes, invalid request (400), short-timeout excluding gamma, and metrics endpoint with `carrier_gateway_` prefix.
**Why:** Accurate test naming and coverage of the full stack end-to-end without external dependencies.
**Next:** —

---

### 2026-03-12 — Replace context.Background() with t.Context()/b.Context() in tests
**Status:** done
**Files touched:**
- `internal/circuitbreaker/breaker_test.go` — 9 replacements, removed `"context"` import
- `internal/adapter/mock_carrier_test.go` — 5 replacements
- `internal/adapter/http_carrier_test.go` — 5 replacements
- `internal/adapter/adapter_test.go` — 3 replacements (2 in goroutines: captured ctx before closure), removed `"context"` import
- `internal/orchestrator/orchestrator_concurrency_test.go` — 9 replacements (goroutine + benchmark captured ctx before closure)
- `internal/orchestrator/orchestrator_test.go` — 4 replacements, removed `"context"` import
- `internal/ratelimiter/limiter_test.go` — 3 replacements
- `internal/cleanup/ticker_test.go` — 3 replacements
**What:** Replaced all 41 `context.Background()` calls in test files with `t.Context()` (or `b.Context()` in benchmarks). Go 1.24+ `t.Context()` returns a context auto-cancelled when the test ends, preventing leaked goroutines. Removed unused `"context"` imports from 3 files. Goroutine closures capture `ctx` before the `go func` to avoid calling `t.Context()` from a non-test goroutine.
**Why:** Go skill convention: test code should use `t.Context()` instead of `context.Background()`.
**Next:** —

---

### 2026-03-12 — CI/CD pipeline (GitHub Actions + golangci-lint config)
**Status:** done
**Files touched:**
- `.github/workflows/ci.yml` (new)
- `.github/workflows/release.yml` (new)
- `.golangci.yml` (new)
**What:** Added CI/CD pipeline:
1. **CI workflow** (`ci.yml`) — triggers on push to `main` and all PRs. Four jobs: lint (golangci-lint), test (race detector + 80% coverage gate), build (binary + Docker image), scan (Trivy on built image).
2. **Release workflow** (`release.yml`) — triggers on `v*` tag push. Docker publish to GHCR and changelog generation are scaffolded as commented-out jobs with TODO markers (need repo secrets/registry setup). Placeholder job ensures the workflow is valid.
3. **Linter config** (`.golangci.yml`) — enables govet, errcheck, staticcheck, unused, gosimple, ineffassign. 5m timeout.
**Why:** Improvement #1 from improvements.md — the project had no CI/CD pipeline.
**Next:** —
