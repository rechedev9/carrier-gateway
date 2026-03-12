# Carrier Gateway

Multi-carrier insurance quote aggregation engine. Fans out requests to N carriers in parallel, hedges slow responders adaptively, deduplicates results, and returns sorted quotes through a single HTTP endpoint.

Built in Go with zero web frameworks, zero ORMs, and a hexagonal architecture where the domain layer imports nothing outside stdlib.

```
POST /quotes  -->  fan-out to N carriers  -->  hedge slow ones  -->  dedup + sort  -->  JSON response
```

---

## Table of Contents

- [Architecture](#architecture)
- [How It Works](#how-it-works)
- [Getting Started](#getting-started)
- [API Reference](#api-reference)
- [Configuration](#configuration)
- [Deployment](#deployment)
- [Observability](#observability)
- [Security](#security)
- [Testing](#testing)
- [Project Structure](#project-structure)

---

## Architecture

```
                       +-----------------+
                       |  HTTP Handler   |  POST /quotes
                       | (net/http 1.22) |  GET /metrics, /healthz, /readyz
                       +--------+--------+
                                |
                     middleware chain
                  (audit -> security -> auth -> concurrency)
                                |
                   +------------v-----------+
                   |     Orchestrator       |
                   | fan-out / fan-in       |
                   | singleflight dedup     |
                   | hedge monitor (5ms)    |
                   | quote caching (PG)     |
                   +--+------+------+------++
                      |      |      |      |
               +------+  +---+--+  ++-+  +-+-------+
               | Rate |  | Circ |  |EMA|  | Adapter |
               | Limit|  | Brkr |  |p95|  | Registry|
               +------+  +------+  +---+  +-+--+--++
                                           |   |   |
            .------------------------------'   |   '----.
            |               .------------------'         |
      +-----v----+   +-----v----+   +------v-----+   +-----v----+
      |  Alpha   |   |   Beta   |   |   Gamma    |   |  Delta   |
      | 50ms/0%  |   | 200ms/10%|   |  800ms/0%  |   | HTTP/ext |
      +----------+   +----------+   +------------+   +----------+
        (mock)          (mock)          (mock)         (real HTTP)
```

**Layer rules** -- imports are strictly one-directional:

```
domain --> ports --> {circuitbreaker, ratelimiter, orchestrator, adapter, metrics, handler, middleware}
```

- `domain` -- pure value types, sentinel errors. Imports only `errors` and `time`.
- `ports` -- interfaces (`CarrierPort`, `OrchestratorPort`, `MetricsRecorder`, `QuoteRepository`). Imports only `domain` + stdlib.
- Everything else implements ports. No package imports domain backward.

---

## How It Works

### 1. Lock-Free Circuit Breaker (3-State Machine)

Three states (Closed / Open / HalfOpen) managed entirely via `sync/atomic.Int32` and CAS operations. No mutexes. HalfOpen permits exactly one probe goroutine via atomic compare-and-swap on an in-flight counter. Every state transition emits a Prometheus gauge update inline.

> `internal/circuitbreaker/breaker.go`

### 2. Adaptive Hedging (EMA p95 Per Carrier)

Each carrier has an `EMATracker` maintaining an exponentially-weighted p95 latency estimate using `atomic.Pointer` for lock-free reads. A hedge monitor goroutine ticks every 5ms, checks each pending carrier against its `P95 * multiplier` threshold, and fires a hedge request to the fastest available alternative. Hedging is suppressed during warm-up (first N observations).

> `internal/orchestrator/hedging.go`

### 3. Type-Erased Adapter Registry (Zero Reflection)

`Adapter[Req, Resp]` is a generic interface. The `Register` function captures concrete types into `AdapterFunc` closures -- `func(context.Context, QuoteRequest) (QuoteResult, error)`. The `Registry` stores closures by carrier ID. Full type safety at the generic boundary, full erasure at the registry boundary. Zero reflection.

> `internal/adapter/adapter.go`

### 4. Fan-Out / Fan-In (Structured Concurrency)

The orchestrator filters eligible carriers by coverage capability, spawns one goroutine per carrier via `errgroup.WithContext`, and collects results on a buffered channel (`2 * len(eligible)`). A parallel hedge monitor watches for slow carriers. Results are deduplicated by carrier ID (first arrival wins) and sorted by premium ascending. Concurrent requests with the same `request_id` are deduplicated via `singleflight`.

> `internal/orchestrator/orchestrator.go`

### 5. Per-Carrier Rate Limiting (Token Bucket)

Each carrier gets its own `rate.Limiter` (`golang.org/x/time/rate`) with independent tokens-per-second and burst. Blocking mode for primaries, non-blocking for hedges. Rejections emit Prometheus counters.

> `internal/ratelimiter/limiter.go`

---

## Getting Started

### Prerequisites

- Go 1.25+
- Docker & Docker Compose (optional, for full stack)
- PostgreSQL 16+ (optional, for quote caching)

### Run Locally

```bash
# Build
go build ./cmd/carrier-gateway

# Start with mock carriers (no DB required)
API_KEYS=my-secret-key go run ./cmd/carrier-gateway

# Custom port
API_KEYS=my-secret-key go run ./cmd/carrier-gateway -addr :9090
```

### Run with Docker Compose (Full Stack)

Spins up the gateway, PostgreSQL, Prometheus, and Grafana:

```bash
cp .env.example .env
# Edit .env -- at minimum set API_KEYS

docker compose up --build
```

| Service    | URL                        |
|------------|----------------------------|
| Gateway    | http://localhost:8080       |
| Prometheus | http://localhost:9090       |
| Grafana    | http://localhost:3000       |

---

## API Reference

### POST /quotes

Request quotes from all eligible carriers.

**Headers:**

| Header          | Required | Description                          |
|-----------------|----------|--------------------------------------|
| `Authorization` | Yes      | `Bearer <api-key>`                   |
| `Content-Type`  | Yes      | `application/json`                   |
| `X-Request-ID`  | No       | Fallback request ID (pre-body parse) |

**Request body:**

```json
{
  "request_id": "demo-1",
  "coverage_lines": ["auto", "homeowners"],
  "timeout_ms": 5000
}
```

| Field            | Type       | Required | Default | Description                                          |
|------------------|------------|----------|---------|------------------------------------------------------|
| `request_id`     | `string`   | Yes      | --      | Idempotency key. Same ID returns cached results.     |
| `coverage_lines` | `string[]` | Yes      | --      | `auto`, `homeowners`, `umbrella`                     |
| `timeout_ms`     | `int`      | No       | `5000`  | Max wait time for carrier responses (100 -- 30000).  |

**Response (200):**

```json
{
  "request_id": "demo-1",
  "quotes": [
    {
      "carrier_id": "alpha",
      "carrier_ref": "",
      "premium_cents": 125000,
      "currency": "USD",
      "is_hedged": false,
      "latency_ms": 48
    },
    {
      "carrier_id": "beta",
      "premium_cents": 187500,
      "currency": "USD",
      "is_hedged": true,
      "latency_ms": 210
    }
  ],
  "duration_ms": 215
}
```

**Error responses:**

| Status | Code                    | When                                     |
|--------|-------------------------|------------------------------------------|
| 400    | `INVALID_JSON`          | Malformed JSON body                      |
| 400    | `INVALID_REQUEST`       | Missing/invalid fields                   |
| 400    | `REQUEST_TOO_LARGE`     | Body exceeds 1 MB                        |
| 401    | `UNAUTHORIZED`          | Missing or invalid API key               |
| 422    | `NO_ELIGIBLE_CARRIERS`  | No carriers match requested coverage     |
| 504    | `TIMEOUT`               | All carriers timed out                   |
| 503    | --                      | Concurrency limit reached (has `Retry-After: 1`) |

### GET /healthz

Liveness probe. Always returns `200 ok`.

### GET /readyz

Readiness probe. Returns `200 ok` when healthy, `503 db: unreachable` when the database connection fails. When no database is configured, always returns 200.

### GET /metrics

Prometheus metrics endpoint (isolated registry).

### Curl Examples

```bash
# Get quotes for auto coverage
curl -s -X POST http://localhost:8080/quotes \
  -H 'Authorization: Bearer my-secret-key' \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"demo-1","coverage_lines":["auto"]}' | jq .

# Short timeout (500ms) -- may exclude slow carriers
curl -s -X POST http://localhost:8080/quotes \
  -H 'Authorization: Bearer my-secret-key' \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"demo-2","coverage_lines":["auto","homeowners"],"timeout_ms":500}' | jq .

# Prometheus metrics
curl -s http://localhost:8080/metrics | grep carrier_
```

---

## Configuration

All configuration is via environment variables. No config files.

| Variable               | Default  | Description                                                  |
|------------------------|----------|--------------------------------------------------------------|
| `ADDR`                 | `:8080`  | HTTP listen address (overridden by `-addr` flag)             |
| `API_KEYS`             | --       | **Required.** Comma-separated bearer tokens. Gateway exits if unset. |
| `DATABASE_URL`         | --       | PostgreSQL DSN. When unset, runs without persistence.        |
| `DELTA_BASE_URL`       | --       | Base URL for Delta HTTP carrier. Omit to disable.            |
| `DELTA_API_KEY`        | --       | API key for Delta carrier.                                   |
| `LOG_LEVEL`            | `info`   | Structured log level (`debug`, `info`, `warn`, `error`).     |
| `CLEANUP_INTERVAL`     | `5m`     | How often expired quotes are purged from the database.        |
| `MAX_CONCURRENT_QUOTES`| `100`    | Max in-flight requests before 503.                           |

---

## Deployment

### Docker

```bash
docker build -t carrier-gateway .
docker run -p 8080:8080 -e API_KEYS=my-key carrier-gateway
```

Runtime image is `distroless/static:nonroot` -- near-zero attack surface, includes TLS root certificates.

### Kubernetes

Production-ready manifests in `deploy/k8s/`:

| Manifest                  | What it creates                                              |
|---------------------------|--------------------------------------------------------------|
| `namespace.yaml`          | `carrier-gateway` namespace                                  |
| `config.yaml`             | ConfigMap + Secret for env vars                              |
| `gateway.yaml`            | Deployment (2 replicas, rolling update, security context) + Service |
| `scaling-and-policy.yaml` | HPA (2-10 replicas, CPU 70%), PDB, NetworkPolicy             |

Key settings: nonroot/read-only filesystem, liveness+readiness probes on `/healthz` and `/readyz`, topology spread constraints, 35s termination grace period.

```bash
kubectl apply -f deploy/k8s/
```

---

## Observability

### Prometheus Metrics

8 metrics registered on an isolated Prometheus registry:

| Metric                                   | Type      | Labels                          |
|------------------------------------------|-----------|---------------------------------|
| `carrier_circuit_breaker_state`          | Gauge     | `carrier_id`                    |
| `carrier_p95_latency_ms`                | Gauge     | `carrier_id`                    |
| `carrier_quote_latency_seconds`          | Histogram | `carrier_id`, `status`          |
| `orchestrator_fan_out_duration_seconds`  | Histogram | --                              |
| `carrier_requests_total`                 | Counter   | `carrier_id`, `status`          |
| `hedge_requests_total`                   | Counter   | `carrier_id`, `trigger_carrier` |
| `circuit_breaker_transitions_total`      | Counter   | `carrier_id`, `from_state`, `to_state` |
| `rate_limit_exceeded_total`              | Counter   | `carrier_id`                    |

### Alerting Rules

5 Prometheus alert rules in `deploy/prometheus/alerts.yml`:

| Alert                 | Severity | Condition                           |
|-----------------------|----------|-------------------------------------|
| `HighLatencyP99`      | critical | P99 latency > 2s for 5m             |
| `HighErrorRate`       | critical | Error rate > 5% for 5m              |
| `CircuitBreakerOpen`  | warning  | CB state == open for 5m             |
| `ExcessiveHedging`    | warning  | Hedge ratio > 20% for 5m            |
| `RateLimitingActive`  | warning  | Rate limit rejections > 10/min for 5m |

### Grafana

Pre-provisioned Grafana dashboards in `deploy/grafana/`. Auto-configured as a Prometheus data source in Docker Compose.

### Structured Logging

JSON-formatted structured logs via `log/slog`. Level configurable at runtime via `LOG_LEVEL` env var.

---

## Security

- **API key authentication** -- `Authorization: Bearer <key>` on all endpoints except `/healthz`, `/readyz`, `/metrics`. Constant-time comparison (`crypto/subtle`). Gateway refuses to start without `API_KEYS` configured (fail-closed).
- **Security headers** -- `X-Content-Type-Options: nosniff`, HSTS.
- **Audit logging** -- every request logged with method, path, status, duration, client identity.
- **Concurrency limiting** -- semaphore-based middleware caps in-flight requests (default 100), returns `503 + Retry-After`.
- **Request size limiting** -- body capped at 1 MB.
- **Distroless runtime** -- `gcr.io/distroless/static:nonroot` Docker image with read-only filesystem in K8s.
- **Network policy** -- K8s NetworkPolicy restricts ingress/egress to known ports and namespaces.

---

## Testing

```bash
# All tests with race detector
go test -race -count=1 ./...

# Single package
go test -race ./internal/circuitbreaker/...

# Stress test orchestrator concurrency
go test -race -run TestOrchestrator -count=5 ./internal/orchestrator/...

# Static analysis
go vet ./...

# Format check
gofmt -l .
```

13 test files, ~2800 lines of test code covering:

- Unit tests for every package (circuit breaker, rate limiter, EMA tracker, adapter registry, handler, middleware)
- Concurrency stress tests with race detector
- End-to-end tests booting the full composition root via `httptest.Server`
- Goroutine leak detection via `goleak`
- Benchmarks for fan-out throughput

### CI Pipeline

GitHub Actions workflow (`.github/workflows/ci.yml`):

1. **Lint** -- golangci-lint with govet, errcheck, staticcheck, unused, gosimple, ineffassign
2. **Test** -- race detector + 80% coverage gate
3. **Build & Scan** -- binary + Docker image + Trivy vulnerability scanner

---

## Project Structure

```
cmd/carrier-gateway/
    main.go                  Composition root, signal handling, graceful shutdown
    e2e_test.go              Full-stack end-to-end tests

internal/
    domain/                  Value types, sentinel errors (stdlib only)
    ports/                   Interfaces: CarrierPort, OrchestratorPort, MetricsRecorder, QuoteRepository
    circuitbreaker/          Lock-free 3-state atomic circuit breaker
    ratelimiter/             Per-carrier token bucket (golang.org/x/time/rate)
    orchestrator/            Fan-out engine, singleflight dedup, EMA hedge tracker
    adapter/                 Generic Adapter[Req,Resp], Registry, mock carriers, HTTP carrier, Delta adapter
    metrics/                 Prometheus recorder (8 metrics, isolated registry)
    handler/                 HTTP handler (POST /quotes, GET /metrics, /healthz, /readyz)
    middleware/              Auth, security headers, audit log, concurrency limiter
    repository/              PostgreSQL quote cache (optional)
    cleanup/                 Background ticker for expired quote purge
    testutil/                Shared test helpers (NoopRecorder)

deploy/
    k8s/                     Kubernetes manifests (Deployment, HPA, PDB, NetworkPolicy)
    prometheus/              Prometheus config + 5 alert rules
    grafana/                 Dashboard provisioning

db/migrations/              SQL migrations (idempotent)
.github/workflows/          CI (lint + test + build + scan) and release pipeline
```

---

## Dependencies

| Dependency                       | Purpose                        |
|----------------------------------|--------------------------------|
| `github.com/lib/pq`             | PostgreSQL driver              |
| `github.com/prometheus/client_golang` | Prometheus metrics        |
| `golang.org/x/sync`             | `errgroup`, `singleflight`     |
| `golang.org/x/time`             | Token bucket rate limiter      |
| `go.uber.org/goleak`            | Goroutine leak detection (test)|

No web frameworks. No ORMs. No DI containers.
