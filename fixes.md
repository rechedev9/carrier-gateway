# fixes.md

Actionable fixes from the post-MVP audit. Ordered by severity.
Each item references the audit finding ID, the file(s) to change, and the exact fix.

---

## Critical

### FIX-C1 — `/metrics` serves wrong registry; carrier metrics never exposed
**Audit ref:** C1
**Files:** `internal/handler/http.go`, `cmd/carrier-gateway/main.go`

**Problem:** `promhttp.Handler()` serves `prometheus.DefaultGatherer` (the global registry).
All 8 carrier metrics are registered in an isolated `prometheus.NewRegistry()` in `main.go`.
They are computed and stored but never visible at `/metrics`.

**Fix:**
1. Add a `prometheus.Gatherer` field to `Handler` (or accept it in `New`).
2. Replace `promhttp.Handler()` with `promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})`.
3. Pass `reg` (the isolated registry) from `main.go` when constructing the handler.

```go
// handler.go — New signature
func New(orch ports.OrchestratorPort, m ports.MetricsRecorder, gatherer prometheus.Gatherer, log *slog.Logger) *Handler

// handler.go — RegisterRoutes
mux.HandleFunc("GET /metrics", promhttp.HandlerFor(h.gatherer, promhttp.HandlerOpts{}).ServeHTTP)

// main.go
h := handler.New(orch, rec, reg, log)
```

---

## High

### FIX-H1 — DB connection never closed on graceful shutdown
**Audit ref:** H1
**File:** `cmd/carrier-gateway/main.go`

**Problem:** `repository.Open(dsn)` returns a `*sql.DB` that is never closed.
Postgres holds connection-pool slots until the OS reclaims them after the process exits.

**Fix:** Defer `db.Close()` immediately after the open succeeds.

```go
db, err := repository.Open(dsn)
if err != nil { ... } else {
    defer db.Close()   // ← add this
    pg := repository.New(db)
    ...
}
```

---

### FIX-H2 — `DeleteExpired` never called — quotes table grows without bound
**Audit ref:** H2
**File:** `cmd/carrier-gateway/main.go`

**Problem:** `ports.QuoteRepository.DeleteExpired` is implemented but never invoked.
Every `Save` call inserts rows; nothing removes expired ones.

**Fix:** Start a background goroutine in `main.go` that calls `repo.DeleteExpired`
on a fixed interval and exits when the shutdown signal fires.

```go
if repo != nil {
    go func() {
        ticker := time.NewTicker(1 * time.Hour)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                n, err := repo.DeleteExpired(context.Background())
                if err != nil {
                    log.Warn("delete expired quotes failed", slog.String("error", err.Error()))
                } else {
                    log.Info("deleted expired quotes", slog.Int64("count", n))
                }
            case <-sigCtx.Done():
                return
            }
        }
    }()
}
```

---

### FIX-H3 — Error logs use `X-Request-ID` header instead of body `request_id`
**Audit ref:** H3
**File:** `internal/handler/http.go`

**Problem:** `handleOrchError` and `writeError` both call `r.Header.Get("X-Request-ID")`.
The header is not required. All error-path log entries have an empty `request_id`
unless the client sets that header explicitly. The correct ID is already parsed
from the body and available in the calling context.

**Fix:** Thread `requestID string` into both helpers. Pass `domainReq.RequestID`
from `handlePostQuotes` after the body is parsed; for pre-parse errors (malformed JSON,
oversized body) fall back to the header.

```go
// Before parse succeeds — fall back to header
h.writeError(w, r, http.StatusBadRequest, r.Header.Get("X-Request-ID"), "INVALID_JSON", ...)

// After parse succeeds — use body's request_id
h.writeError(w, r, http.StatusBadRequest, domainReq.RequestID, "INVALID_REQUEST", ...)

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, status int, requestID, code, message string) {
    attrs := []slog.Attr{
        slog.String("request_id", requestID),
        ...
    }
```

---

### FIX-H4 — Zero `EMAAlpha` + zero `EMAWindowSize` silently freezes the tracker
**Audit ref:** H4
**File:** `internal/orchestrator/hedging.go`

**Problem:** If both fields are zero, `alpha = 0.0`. Every `Record` call computes
`newP95 = 0*latency + 1*oldP95 = oldP95` — the tracker never updates.
`HedgeThreshold` returns `2×seed` forever with no warning.

**Fix:** Add a guard in `NewEMATracker` after alpha is resolved.

```go
if alpha <= 0 || alpha >= 1 {
    alpha = 0.1 // documented default
    // optionally: log.Warn(...)
}
```

---

## Medium

### FIX-M1 — Hedge goroutines untracked — panic risk on send to closed channel
**Audit ref:** M1
**File:** `internal/orchestrator/hedging.go`

**Problem:** Hedge goroutines are fired with `go func() { ... }()` and are not
tracked by the errgroup. After `g.Wait()` returns, `gCtx` is cancelled and
`results` is immediately closed by the drain goroutine. An adapter that ignores
context cancellation and returns a result after `results` is closed will panic
(send on closed channel).

**Fix:** Track hedge goroutines with a `sync.WaitGroup` local to `hedgeMonitor`.
Wait for all hedge goroutines to finish before `hedgeMonitor` itself returns,
so the drain goroutine can't close `results` while a hedge send is still possible.

```go
var hedgeWg sync.WaitGroup
// when firing:
hedgeWg.Add(1)
go func() {
    defer hedgeWg.Done()
    ...
}()
// at the end of hedgeMonitor, after the loop:
hedgeWg.Wait()
```

---

### FIX-M2 — Sub-millisecond truncation corrupts EMA
**Audit ref:** M2
**File:** `internal/orchestrator/hedging.go:72`

**Problem:** `latency.Milliseconds()` returns `int64` — truncates to zero for anything
under 1ms. Fast carriers in tests run at 5–10ms (fine), but hedged responses or
future low-latency carriers could feed zeros into the EMA, pulling `p95` toward zero
and causing spurious hedge storms.

**Fix:** One-line change.

```go
// Before
latencyMs := float64(latency.Milliseconds())

// After
latencyMs := float64(latency) / float64(time.Millisecond)
```

---

### FIX-M3 — `ReadTimeout` missing from `http.Server`
**Audit ref:** M3
**File:** `cmd/carrier-gateway/main.go`

**Problem:** `ReadHeaderTimeout` is set (10s) but `ReadTimeout` is not.
A client can drip-feed the request body indefinitely, holding a connection
for the full `WriteTimeout` window (35s). `MaxBytesReader` limits size but not rate.

**Fix:**

```go
srv := &http.Server{
    Addr:              *addr,
    Handler:           mux,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       15 * time.Second, // ← add
    WriteTimeout:      35 * time.Second,
    IdleTimeout:       60 * time.Second,
}
```

---

### FIX-M4 — No test for nil-exec guard in hedgeable loop
**Audit ref:** M4
**File:** `internal/orchestrator/orchestrator_test.go`

**Problem:** The guard added in Bug 2 (`execFn, ok := o.registry.Get(carrierID); if !ok { continue }`)
has zero test coverage. A regression would re-introduce a nil-dereference panic in the hedge path.

**Fix:** Add a test that constructs an orchestrator with a carrier in the `carriers` slice
but intentionally omits it from the registry. Call `GetQuotes` and assert:
1. No panic.
2. The carrier is absent from results (other carriers still respond).
3. An error is logged (check `NoopRecorder` or inject a log capture).

---

### FIX-M5 — Jitter can produce negative latency in `MockCarrier`
**Audit ref:** M5
**File:** `internal/adapter/mock_carrier.go:96`

**Problem:** If `JitterMs > BaseLatency.Milliseconds()`, `latency` goes negative.
`time.NewTimer(negative)` fires immediately — no sleep, instant response.

**Fix:**

```go
if latency < 0 {
    latency = 0
}
```

---

## Low

### FIX-L1 — Replace `slog.LevelError + 1` magic number in tests
**Audit ref:** L1
**Files:** `internal/adapter/adapter_test.go:22`, `internal/adapter/http_carrier_test.go:18`,
`internal/orchestrator/orchestrator_test.go:30`

**Fix:** Use `io.Discard` as the writer instead of a magic level offset.

```go
// Before
slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))

// After
slog.New(slog.NewTextHandler(io.Discard, nil))
```

---

### FIX-L2 — `DeltaResponse.QuoteID` is discarded
**Audit ref:** L2
**File:** `internal/adapter/delta_carrier.go`, `internal/domain/quote.go`

**Problem:** Delta returns a `QuoteID` (their internal reference). It is not surfaced
in `domain.QuoteResult`. Consumers cannot reference the quote for binding or auditing.

**Fix (if needed):** Add `CarrierRef string` to `domain.QuoteResult` and populate it
in `deltaAdapter.FromCarrierResponse`. Field should be optional (empty for mock carriers).

---

### FIX-L3 — Stale architecture comment in `CLAUDE.md`
**Audit ref:** L3
**File:** `CLAUDE.md:45`

**Problem:** Comment says `hedgeMonitor goroutine ticks every 5ms` — no longer accurate
after Bug 1 fix. Tick interval is now `Config.HedgePollInterval`, defaulting to 5ms.

**Fix:** Update the line to:
```
hedgeMonitor goroutine ticks every Config.HedgePollInterval (default 5ms).
```

---

### FIX-L4 — `orchestrator.Config{}` in `main.go` hides the default
**Audit ref:** L4
**File:** `cmd/carrier-gateway/main.go:144`

**Problem:** Passing an empty struct relies silently on `New()` defaulting
`HedgePollInterval` to 5ms. Intent is invisible.

**Fix option A:** Export a `DefaultConfig()` function from the orchestrator package.
**Fix option B:** Be explicit: `orchestrator.Config{HedgePollInterval: 5 * time.Millisecond}`.

---

## Fix priority order

| Priority | Fix | Effort |
|---|---|---|
| 1 | FIX-C1 — metrics registry | Small (3 lines changed) |
| 2 | FIX-H1 — db.Close() | Trivial (1 line) |
| 3 | FIX-H3 — request_id in error logs | Small |
| 4 | FIX-H2 — DeleteExpired background loop | Small |
| 5 | FIX-H4 — frozen EMA guard | Trivial |
| 6 | FIX-M2 — sub-ms truncation | Trivial (1 line) |
| 7 | FIX-M3 — ReadTimeout | Trivial (1 line) |
| 8 | FIX-M1 — hedge goroutine WaitGroup | Medium |
| 9 | FIX-M5 — jitter clamp | Trivial |
| 10 | FIX-M4 — nil-exec test | Small |
| 11 | FIX-L1 — magic log level | Trivial |
| 12 | FIX-L3 — stale CLAUDE.md comment | Trivial |
| 13 | FIX-L2 — DeltaQuoteID | Medium (domain change) |
| 14 | FIX-L4 — explicit orchestrator config | Trivial |
