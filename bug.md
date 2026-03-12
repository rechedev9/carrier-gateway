# Post-MVP Bug Report

Bugs found and fixed after the initial `carrier-gateway` MVP commit (`aacde5e`).
All four were introduced during the initial build — none were regressions.

---

## Bug 1 — `hedgeMonitor` ignores `Config.HedgePollInterval` (behavioral)

**File:** `internal/orchestrator/hedging.go`, `internal/orchestrator/orchestrator.go`

### What was wrong

`hedgeMonitor` hard-coded its ticker to a package-level constant:

```go
// hedging.go
const hedgePollInterval = 5 * time.Millisecond
ticker := time.NewTicker(hedgePollInterval) // constant — never the config value
```

`Config.HedgePollInterval` was properly validated and defaulted in `New()`, but was never
passed to `hedgeMonitor` as a parameter. Any caller that set a custom poll interval (e.g.
tests that needed a faster or slower tick) silently got 5ms regardless.

### Why it happened

The constant and the config field were added at the same time. The config field was wired
up at the struct level but the author forgot to thread it through to the leaf function. The
plumbing *looked* complete on inspection of `orchestrator.go` alone.

### Fix

1. Added `pollInterval time.Duration` parameter to `hedgeMonitor`.
2. Passed `o.cfg.HedgePollInterval` at the call site in `GetQuotes`.
3. Deleted the now-dead `hedgePollInterval` package constant.

---

## Bug 2 — Nil `exec` not guarded in hedge path (potential panic)

**File:** `internal/orchestrator/orchestrator.go`

### What was wrong

When building the `hedgeable` slice, the `ok` return from `registry.Get` was discarded:

```go
execFn, _ := o.registry.Get(carrierID) // ok silently dropped
hedgeable = append(hedgeable, hedgeCarrier{
    exec: adapterExecFn(execFn), // execFn may be nil
})
```

If a carrier passed `filterEligibleCarriers` (CB not Open) but had no registered adapter,
`execFn` would be `nil`. When `hedgeMonitor` later selected that carrier as a hedge
candidate and called the exec func, the process would panic.

The identical path in `callCarrier` already guarded correctly:

```go
execFn, ok := o.registry.Get(carrier.ID)
if !ok {
    o.log.Error("no adapter registered for carrier", ...)
    return nil
}
```

### Why it happened

Copy-paste inconsistency. The hedge-build loop was written separately from `callCarrier`
and the author didn't apply the same defensive check, likely because the panic path requires
a configuration mistake (carrier in the carrier list but not in the registry) that wouldn't
surface during normal testing with the provided mock carriers.

### Fix

Mirrored `callCarrier`'s guard: skip the carrier with an error log if `ok` is false.

---

## Bug 3 — Unnecessary `string([]byte)` copy in HTTP request body (performance)

**File:** `internal/adapter/http_carrier.go:123`

### What was wrong

```go
// Before — allocates a new string copy of the already-marshalled payload
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
```

`payload` is `[]byte` from `json.Marshal`. Converting it to `string` creates a full copy
in memory before wrapping it in a reader. This is unnecessary — `bytes.NewReader` accepts
`[]byte` directly and does not copy.

### Why it happened

Likely written by reflex — `strings.NewReader` is the more commonly seen idiom for small
string literals in examples. The author applied it without noticing that `payload` was
already a `[]byte`.

### Fix

```go
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
```

Added `"bytes"` to imports.

---

## Bug 4 — `github.com/lib/pq` wrongly annotated `// indirect` in `go.mod`

**File:** `go.mod`

### What was wrong

```
github.com/lib/pq v1.11.2 // indirect
```

`internal/repository/postgres.go` contains a direct blank import:

```go
import _ "github.com/lib/pq"
```

A direct blank import is a direct dependency. Marking it `// indirect` is incorrect — it
misleads `go mod tidy`, tooling that audits dependencies, and any developer reading the
module graph.

### Why it happened

The dependency was likely added manually (e.g. `go get github.com/lib/pq`) and `go mod tidy`
was not run afterward, or it was added to the wrong `require` block by hand.

### Fix

Ran `go mod tidy`, which promoted `lib/pq` to the direct `require` block without the
`// indirect` annotation.
