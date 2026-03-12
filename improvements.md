# Production Readiness Improvements

> Full audit of what the carrier-gateway needs to reach production-grade.
> Current state: strong engineering foundations (hexagonal arch, lock-free concurrency, 80-91% test coverage), but significant operational gaps.

---

## 1. CI/CD Pipeline (currently: none)

- [ ] **GitHub Actions workflow** — lint (`golangci-lint`), `go vet`, `go test -race`, `go build` on every PR
- [ ] **Build matrix** — test against Go 1.22+ on linux/amd64 and linux/arm64
- [ ] **Docker image publish** — build + push to GHCR/ECR on merge to main
- [ ] **Image scanning** — Trivy or Grype for CVE detection on built images
- [ ] **Coverage gate** — fail PR if coverage drops below 80%
- [ ] **Semantic versioning** — tag releases, generate changelogs from commit history
- [ ] **Staged deployment** — deploy to staging on merge, promote to prod with approval gate

---

## 2. Authentication & Authorization (currently: none)

- [ ] **API key authentication** — middleware that validates `Authorization: Bearer <key>` on `/quotes`
- [ ] **Per-client rate limiting** — tie rate limits to API key identity, not just per-carrier
- [ ] **Audit logging** — log every request with client identity, timestamp, response code
- [ ] **mTLS option** — for service-to-service communication in internal deployments
- [ ] **CORS policy** — if browser clients ever need direct access
- [ ] **Security headers** — `X-Content-Type-Options: nosniff`, `Strict-Transport-Security`

---

## 3. Observability (currently: metrics + basic logging)

### Distributed Tracing
- [ ] **OpenTelemetry SDK** — instrument handler, orchestrator, adapter, and DB layers with spans
- [ ] **Trace context propagation** — inject `traceparent` header into outbound carrier HTTP calls
- [ ] **Trace exporter** — Jaeger or OTLP collector in docker-compose and k8s

### Alerting
- [ ] **Prometheus alert rules** — define in `deploy/prometheus/alerts.yml`:
  - `HighLatencyP99` — p99 quote latency > 2s for 5m
  - `HighErrorRate` — error rate > 5% for 5m
  - `CircuitBreakerOpen` — any carrier CB open > 5m
  - `ExcessiveHedging` — hedge ratio > 20%
  - `RateLimitingActive` — rate limit rejections > 10/min
- [ ] **Alertmanager** — route alerts to Slack/PagerDuty

### Dashboards
- [ ] **Grafana dashboard JSON** — pre-built panels for latency, error rate, CB state, hedge ratio, rate limit usage, goroutine count
- [ ] **Metrics glossary** — document what each of the 8 metrics means and when to worry

### Logging Enhancements
- [ ] **Log level configuration** — `LOG_LEVEL` env var (debug/info/warn/error)
- [ ] **Trace ID in logs** — correlate log lines with distributed traces
- [ ] **Log sampling** — rate-limit repetitive log lines under high QPS

---

## 4. Kubernetes Deployment (currently: Docker only)

- [ ] **Deployment manifest** — replicas, resource requests/limits, rolling update strategy
- [ ] **Service manifest** — ClusterIP for internal traffic, LoadBalancer/Ingress for external
- [ ] **ConfigMap** — externalize carrier configs, timeouts, rate limits (currently hardcoded in `main.go`)
- [ ] **Secrets** — `DELTA_API_KEY`, `DATABASE_URL` via k8s Secret or external secrets operator
- [ ] **Health probes:**
  - Liveness: `GET /healthz` (already exists)
  - Readiness: `GET /readyz` (new — check DB connectivity + at least 1 carrier reachable)
  - Startup: delay readiness until EMA tracker warm-up completes
- [ ] **PodDisruptionBudget** — `minAvailable: 1` to survive node drains
- [ ] **HorizontalPodAutoscaler** — scale on CPU, memory, or custom `carrier_quote_latency_seconds` metric
- [ ] **Pod anti-affinity** — spread replicas across availability zones
- [ ] **NetworkPolicy** — restrict ingress to known sources, egress to carriers + postgres
- [ ] **Helm chart or Kustomize** — templated manifests for dev/staging/prod

---

## 5. Configuration Management (currently: env vars + hardcoded)

- [ ] **Config file support** — YAML/TOML for carrier definitions, timeouts, CB thresholds, rate limits
- [ ] **Config validation at startup** — fail fast with clear error messages on invalid values
- [ ] **Startup config dump** — log resolved configuration (redact secrets) at Info level on boot
- [ ] **Environment-specific overrides** — base config + env-specific overlay (dev/staging/prod)
- [ ] **Secrets management** — HashiCorp Vault or AWS Secrets Manager integration for API keys + DB credentials
- [ ] **Config hot-reload** — watch config file for non-critical tuning changes (rate limits, CB thresholds) without restart

---

## 6. Database Hardening (currently: single-node, no HA)

### High Availability
- [ ] **Streaming replication** — at least 1 synchronous standby
- [ ] **Automated failover** — Patroni, pg_auto_failover, or managed DB (RDS/Cloud SQL)
- [ ] **Read replicas** — offload `FindByRequestID` reads from primary

### Backup & Recovery
- [ ] **Automated backups** — daily `pg_dump` or WAL archiving to S3/GCS
- [ ] **Point-in-time recovery** — WAL-based PITR with defined RPO (e.g., < 1 hour)
- [ ] **Restore testing** — scheduled restore drills to verify backup integrity

### Monitoring
- [ ] **Connection pool metrics** — expose `db.Stats()` (open/idle/waiting) as Prometheus gauges
- [ ] **Query latency metrics** — instrument `Save`, `FindByRequestID`, `DeleteExpired` with histograms
- [ ] **Slow query logging** — log queries exceeding 100ms

### Schema Management
- [ ] **Migration tool** — `golang-migrate` or `goose` with version tracking table
- [ ] **Separate init container** — run migrations before app starts (not inline in `main.go`)

---

## 7. API Improvements

- [ ] **API versioning** — prefix routes with `/v1/` (`/v1/quotes`, `/v1/metrics`)
- [ ] **OpenAPI spec** — machine-readable spec generated from handler code or maintained manually
- [ ] **Richer error codes** — distinguish `CIRCUIT_OPEN`, `RATE_LIMITED`, `CARRIER_TIMEOUT` from generic errors so clients can react appropriately
- [ ] **Response compression** — `gzip` middleware for large response bodies
- [ ] **Request ID generation** — if client omits `X-Request-ID`, generate a UUID and return it in response headers
- [ ] **Readiness endpoint** — `GET /readyz` that checks DB ping + carrier reachability

---

## 8. Resilience Enhancements

- [ ] **Bulkhead isolation** — separate HTTP connection pools per carrier to prevent one slow carrier from starving others
- [ ] **Request concurrency limit** — cap in-flight `/quotes` requests (e.g., semaphore of 100) to prevent goroutine explosion under load
- [ ] **Request deduplication** — `singleflight.Group` for identical concurrent `request_id` values
- [ ] **Fallback cache** — if `repo.Save` fails, buffer quotes in-memory with retry
- [ ] **Timeout budgeting** — subtract elapsed time from retry budget so retries don't exceed the overall deadline
- [ ] **Graceful load shedding** — return `503 Service Unavailable` with `Retry-After` header when at capacity

---

## 9. Testing Gaps

- [ ] **Integration tests with real Postgres** — use `testcontainers-go` to spin up a Postgres container in CI
- [ ] **Load testing** — `k6` or `vegeta` script: sustained QPS ramp, measure p99 latency and error rate under load
- [ ] **Contract tests** — consumer-driven contract test for the Delta carrier API (catch breaking changes)
- [ ] **Chaos testing** — inject carrier timeouts, DB failures, network partitions using `toxiproxy`
- [ ] **Benchmark tracking** — run `BenchmarkOrchestrator` in CI, track regressions over time
- [ ] **Fuzz testing** — `go test -fuzz` on request validation to find edge cases

---

## 10. Operational Readiness

### SLOs & SLIs
- [ ] **Define SLOs:**
  - Availability: 99.9% (measured by non-5xx responses)
  - Latency: p99 < 2s, p50 < 500ms
  - Error rate: < 1% of requests
- [ ] **SLI instrumentation** — derive SLIs from existing Prometheus metrics
- [ ] **Error budget tracking** — dashboard showing remaining error budget for the month

### Runbooks
- [ ] **Incident response playbook** — escalation path, war room setup, post-incident review template
- [ ] **Alert-specific runbooks:**
  - Circuit breaker open: check carrier health, review error logs, consider manual reset
  - High latency: check hedge ratio, review carrier p95 trends, scale horizontally
  - High error rate: check carrier availability, review recent deployments, check DB connectivity
  - Rate limiting: review client traffic patterns, adjust limits or scale
- [ ] **Rollback procedure** — step-by-step instructions for reverting a bad deployment

### Capacity Planning
- [ ] **Load test results** — document max sustained RPS per pod, memory ceiling, DB connection saturation point
- [ ] **Scaling guidance** — "add 1 pod per X RPS" or "increase DB pool by Y per Z concurrent requests"
- [ ] **Cost model** — infrastructure cost per 1M quotes

---

## 11. Documentation

- [ ] **Architecture Decision Records (ADRs)** — document key decisions: why circuit breaker over semaphore, why EMA not sliding window, why hexagonal architecture
- [ ] **Deployment guide** — step-by-step for k8s, docker-compose, and bare metal
- [ ] **Troubleshooting guide** — interpreting metrics, common failure modes, debugging carrier issues
- [ ] **Performance tuning guide** — how to adjust EMA alpha, hedge multiplier, CB thresholds, rate limits
- [ ] **Security policy** — vulnerability disclosure, incident response, dependency update cadence

---

## Priority Order

| Phase | Focus | Effort |
|-------|-------|--------|
| **1** | CI/CD + Alerting + Runbooks | 1-2 weeks |
| **2** | Authentication + K8s manifests | 2-3 weeks |
| **3** | Distributed tracing + Dashboards | 1-2 weeks |
| **4** | Database HA + Backups | 2-3 weeks |
| **5** | Config management + API versioning | 1 week |
| **6** | Load testing + Chaos testing | 1 week |
| **7** | Documentation + ADRs | 1 week |

**Total estimated effort: 10-14 weeks to full production readiness.**
