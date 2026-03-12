# Carrier Gateway — Operational Runbook

## 1. Service Overview

### Endpoints

| Endpoint        | Method | Description                          |
|-----------------|--------|--------------------------------------|
| `/quotes`       | POST   | Fan-out quote request to all eligible carriers |
| `/metrics`      | GET    | Prometheus metrics (isolated registry) |
| `/healthz`      | GET    | Liveness probe — returns `200 "ok"`  |

### Service Topology (docker-compose)

| Service      | Image                    | Port  | Purpose                     |
|--------------|--------------------------|-------|-----------------------------|
| `gateway`    | Local build (Dockerfile) | 8080  | Carrier gateway application |
| `postgres`   | postgres:16-alpine       | 5432  | Quote persistence (optional)|
| `prometheus` | prom/prometheus:v3.3.0   | 9090  | Metrics collection + alerts |
| `grafana`    | grafana/grafana:latest   | 3000  | Dashboards (admin/admin)    |

### Environment Variables

| Variable           | Default                          | Description                        |
|--------------------|----------------------------------|------------------------------------|
| `ADDR`             | `:8080`                          | HTTP listen address                |
| `DATABASE_URL`     | (none)                           | PostgreSQL DSN; omit to run without persistence |
| `DELTA_BASE_URL`   | (none)                           | Delta carrier HTTP endpoint        |
| `DELTA_API_KEY`    | (none)                           | Delta carrier API key              |
| `CLEANUP_INTERVAL` | `5m`                             | Expired-quote cleanup ticker interval |

### Carriers (default)

| ID      | TimeoutHint | Rate Limit (tok/s) | Failure Threshold | Notes              |
|---------|-------------|---------------------|-------------------|--------------------|
| alpha   | 100ms       | 100                 | 5                 | Fast, reliable     |
| beta    | 400ms       | 50                  | 5                 | Medium, 10% error rate |
| gamma   | 1600ms      | 20                  | 5                 | Slow, reliable     |
| delta   | 300ms       | 50                  | 5                 | HTTP carrier, optional |

### Shutdown Sequence

1. Cleanup ticker stopped
2. HTTP server drains in-flight requests (30s timeout)
3. PostgreSQL connection pool closed
4. Process exits (code 0 = clean, code 1 = drain timeout)

---

## 2. Incident Response Playbook

### Severity Levels

| Severity | Criteria                              | Response Time | Examples                          |
|----------|---------------------------------------|---------------|-----------------------------------|
| SEV-1    | Total service outage, no quotes flowing | Immediate   | All CBs open, gateway crash loop  |
| SEV-2    | Degraded — partial quotes or high latency | 15 min     | HighLatencyP99, HighErrorRate     |
| SEV-3    | Warning — single carrier impacted     | 1 hour        | CircuitBreakerOpen (one carrier)  |
| SEV-4    | Informational — operational noise     | Next business day | Rate limiting active, hedge spike |

### Escalation Path

1. **On-call engineer** — triage, apply runbook, mitigate
2. **Team lead** — if root cause unclear after 30 min
3. **Platform team** — if infrastructure (DB, networking) is involved

### War Room Checklist

- [ ] Open Grafana dashboard (`http://localhost:3000`)
- [ ] Open Prometheus alerts page (`http://localhost:9090/alerts`)
- [ ] Check gateway logs: `docker compose logs -f gateway --since 10m`
- [ ] Verify connectivity: `curl -s http://localhost:8080/healthz`
- [ ] Note the time the alert fired and which carrier(s) are affected

### Post-Incident Template

```
## Incident: [Title]
**Date:** YYYY-MM-DD
**Duration:** HH:MM - HH:MM (Xm)
**Severity:** SEV-X
**Alert:** [Alert name]

### Timeline
- HH:MM — Alert fired
- HH:MM — On-call acknowledged
- HH:MM — Root cause identified
- HH:MM — Mitigation applied
- HH:MM — Alert resolved

### Root Cause
[Description]

### Impact
[Number of failed/delayed quotes, affected carriers]

### Action Items
- [ ] [Fix description] — Owner — Due date
```

---

## 3. Alert Runbooks

### 3.1 HighLatencyP99 (critical)

**Fires when:** P99 carrier quote latency > 2s for 5 minutes.

**PromQL:**
```promql
histogram_quantile(0.99, rate(carrier_quote_latency_seconds_bucket[5m])) > 2
```

**Diagnosis:**

1. Identify which carrier is slow:
   ```promql
   histogram_quantile(0.99, rate(carrier_quote_latency_seconds_bucket[5m])) by (carrier_id)
   ```

2. Check if it's a single carrier or system-wide:
   ```promql
   histogram_quantile(0.50, rate(carrier_quote_latency_seconds_bucket[5m])) by (carrier_id)
   ```
   If P50 is also elevated, the carrier is genuinely slow (not just tail latency).

3. Check fan-out duration to see if the orchestrator itself is slow:
   ```promql
   histogram_quantile(0.99, rate(orchestrator_fan_out_duration_seconds_bucket[5m]))
   ```

4. Check if hedging is masking deeper issues:
   ```promql
   rate(hedge_requests_total[5m]) by (carrier_id, trigger_carrier)
   ```

5. Check gateway resource usage: `docker stats gateway`

**Resolution:**

- **Single slow carrier:** The circuit breaker will trip after 5 consecutive failures. Monitor `carrier_circuit_breaker_state` — if it transitions to Open, the carrier is auto-isolated. No action needed unless it stays in HalfOpen cycling.
- **All carriers slow:** Check network (DNS, firewall). Run `curl -w "@curl-format.txt" -s -o /dev/null http://localhost:8080/healthz` to measure gateway latency independently.
- **Gateway resource exhaustion:** Restart gateway: `docker compose restart gateway`. Check PostgreSQL pool (`max_open_conns=25`) is not saturated.

---

### 3.2 HighErrorRate (critical)

**Fires when:** Error rate across all carriers > 5% for 5 minutes.

**PromQL:**
```promql
sum(rate(carrier_requests_total{status!="success"}[5m])) / (sum(rate(carrier_requests_total[5m])) > 0) > 0.05
```

**Diagnosis:**

1. Break down errors by carrier and status:
   ```promql
   rate(carrier_requests_total{status!="success"}[5m]) by (carrier_id, status)
   ```
   Status values: `success`, `error`, `timeout`, `circuit_open`, `rate_limited`.

2. If `circuit_open` is the dominant status, see section 3.3 (CircuitBreakerOpen).

3. If `timeout` is dominant, see section 3.1 (HighLatencyP99) — timeouts are a latency symptom.

4. If `error` is dominant, check gateway logs for the specific carrier:
   ```bash
   docker compose logs gateway --since 10m | grep -i "error"
   ```

5. Check if the error rate correlates with a deployment:
   ```bash
   docker inspect gateway --format '{{.Created}}'
   ```

**Resolution:**

- **Carrier returning errors:** The CB will trip and auto-isolate after 5 failures. If the carrier is external (delta), contact the upstream provider.
- **All carriers erroring:** Check `DATABASE_URL` connectivity. The gateway runs without persistence if DB is unavailable, but logs a warning. If the DB came back with corrupted state, restart: `docker compose restart postgres gateway`.
- **Rate limiting masquerading as errors:** `rate_limited` status counts toward the error rate. Check section 3.5.

---

### 3.3 CircuitBreakerOpen (warning)

**Fires when:** A carrier's circuit breaker state equals 1 (Open) for 5 minutes.

**PromQL:**
```promql
carrier_circuit_breaker_state == 1
```

**Diagnosis:**

1. Identify which carrier(s):
   ```promql
   carrier_circuit_breaker_state by (carrier_id)
   ```
   Values: `0` = Closed, `1` = Open, `2` = HalfOpen.

2. Check the transition history:
   ```promql
   rate(circuit_breaker_transitions_total[15m]) by (carrier_id, from_state, to_state)
   ```
   If you see rapid `halfopen→open` cycling, the carrier is consistently failing its probe requests.

3. Check the carrier's error pattern:
   ```promql
   rate(carrier_requests_total{status!="success"}[5m]) by (carrier_id, status)
   ```

4. Check the carrier's recent latency (before it tripped):
   ```promql
   carrier_p95_latency_ms by (carrier_id)
   ```

**Circuit Breaker State Machine:**
```
Closed --(5 consecutive failures)--> Open
Open --(30s timeout)--> HalfOpen
HalfOpen --(1 probe success)--> Closed
HalfOpen --(1 probe failure)--> Open
```

**Resolution:**

- **Expected behavior:** The CB will auto-recover. After `OpenTimeout` (30s), it transitions to HalfOpen and sends a probe. If the probe succeeds, it closes. No manual intervention needed.
- **Stuck open (>5 min):** The upstream carrier is persistently failing. Check carrier status page or contact carrier support.
- **Rapid cycling:** The carrier is intermittently healthy. The CB is doing its job — isolating bad carriers. If the probe success threshold (`SuccessThreshold=2`) is too aggressive, consider config changes.
- **Mock carriers:** Alpha (0% error rate) should never trip. Beta has 10% error rate — may trip under sustained load. Gamma (0% error rate, 800ms latency) may trip on timeouts if the request timeout is too low.

---

### 3.4 ExcessiveHedging (warning)

**Fires when:** Hedge-to-request ratio > 20% for 5 minutes.

**PromQL:**
```promql
sum(rate(hedge_requests_total[5m])) / (sum(rate(carrier_requests_total[5m])) > 0) > 0.2
```

**Diagnosis:**

1. Identify which carriers are triggering hedges:
   ```promql
   rate(hedge_requests_total[5m]) by (carrier_id, trigger_carrier)
   ```
   `trigger_carrier` is the slow carrier that caused the hedge to fire.

2. Check the EMA p95 latency to see if the hedge threshold is reasonable:
   ```promql
   carrier_p95_latency_ms by (carrier_id)
   ```
   Hedges fire when a carrier exceeds `HedgeMultiplier (1.5) x EMA p95`.

3. Check if the slow carrier is also generating errors:
   ```promql
   rate(carrier_requests_total{status!="success"}[5m]) by (carrier_id)
   ```

**Resolution:**

- **Carrier genuinely slow:** Hedging is doing its job. The system is compensating for a slow carrier by firing redundant requests to faster carriers. Monitor but no action needed unless it's driving rate limiting.
- **Cascade risk:** Excessive hedging increases load on non-slow carriers. Check rate limit rejections (`rate_limit_exceeded_total`) to see if hedges are being throttled.
- **EMA warmup:** During the first 10 observations per carrier, the EMA tracker uses `2x TimeoutHint` as the p95 seed. Hedging thresholds may be inaccurate during warmup (first ~10 requests per carrier after restart).

---

### 3.5 RateLimitingActive (warning)

**Fires when:** Rate limit rejections > 10/min across all carriers for 5 minutes.

**PromQL:**
```promql
sum(rate(rate_limit_exceeded_total[2m])) > 0.167
```

**Diagnosis:**

1. Identify which carrier is being rate-limited:
   ```promql
   rate(rate_limit_exceeded_total[5m]) by (carrier_id)
   ```

2. Cross-reference with the carrier's configured rate limit (see Carriers table in section 1).

3. Check if hedging is driving the rate limiting:
   ```promql
   rate(hedge_requests_total[5m]) by (carrier_id)
   ```
   Hedge requests consume rate limit tokens. A hedge surge can exhaust a carrier's token bucket.

4. Check request volume:
   ```promql
   sum(rate(carrier_requests_total[5m])) by (carrier_id)
   ```

**Resolution:**

- **Hedging-driven:** See section 3.4. Hedging is generating more requests than the carrier's token bucket allows. This is self-correcting — `TryAcquire()` (used for hedge requests) silently suppresses hedges when the bucket is empty.
- **Legitimate traffic spike:** The token bucket refills at `TokensPerSecond`. Gamma has the lowest limit (20 tok/s, burst 2). If sustained traffic exceeds the limit, quotes from that carrier will be dropped. Scale horizontally or increase the rate limit in carrier config.
- **All carriers rate-limited simultaneously:** Unusual — indicates a massive traffic spike. Check upstream load balancer or client behavior.

---

## 4. Common Diagnostic Commands

### Docker Compose

```bash
# View all service status
docker compose ps

# Follow gateway logs (structured JSON)
docker compose logs -f gateway --since 10m

# Restart a single service
docker compose restart gateway

# Full stack restart
docker compose down && docker compose up -d

# Check resource usage
docker stats --no-stream
```

### Health & Connectivity

```bash
# Liveness probe
curl -s http://localhost:8080/healthz

# Quote request (happy path)
curl -s -X POST http://localhost:8080/quotes \
  -H "Content-Type: application/json" \
  -d '{"request_id":"diag-001","coverage_lines":["auto"],"timeout_ms":5000}' | jq .

# Prometheus scrape check
curl -s http://localhost:8080/metrics | head -20

# Check specific metric
curl -s http://localhost:8080/metrics | grep carrier_circuit_breaker_state
```

### PostgreSQL

```bash
# Connect to the database
docker compose exec postgres psql -U carrier -d carrier_gateway

# Check quote table size
docker compose exec postgres psql -U carrier -d carrier_gateway \
  -c "SELECT count(*) FROM quotes;"

# Check expired quotes (cleanup ticker should remove these)
docker compose exec postgres psql -U carrier -d carrier_gateway \
  -c "SELECT count(*) FROM quotes WHERE expires_at < now();"

# Check connection pool usage (from inside postgres)
docker compose exec postgres psql -U carrier -d carrier_gateway \
  -c "SELECT count(*) FROM pg_stat_activity WHERE datname='carrier_gateway';"
```

### Prometheus

```bash
# Check alert status
curl -s http://localhost:9090/api/v1/alerts | jq '.data.alerts[] | {alertname: .labels.alertname, state: .state}'

# Check targets (is the gateway being scraped?)
curl -s http://localhost:9090/api/v1/targets | jq '.data.activeTargets[] | {job: .labels.job, health: .health}'

# Ad-hoc PromQL query
curl -s 'http://localhost:9090/api/v1/query?query=carrier_circuit_breaker_state' | jq .
```

---

## 5. Rollback Procedure

### Docker Compose (current)

1. Identify the last known-good image tag or commit:
   ```bash
   docker compose exec gateway cat /etc/os-release 2>/dev/null || echo "distroless — check docker inspect"
   docker inspect gateway --format '{{.Image}}'
   ```

2. Rebuild from a specific commit:
   ```bash
   git checkout <known-good-commit>
   docker compose build gateway
   docker compose up -d gateway
   ```

3. Verify:
   ```bash
   curl -s http://localhost:8080/healthz
   curl -s http://localhost:8080/metrics | grep carrier_circuit_breaker_state
   ```

### CI/CD (future — GitHub Actions)

When the release workflow is fully configured:

1. Identify the last good image tag in GHCR
2. Update the `docker-compose.yml` image field (or deploy manifest) to the good tag
3. `docker compose pull gateway && docker compose up -d gateway`

### Database Rollback

The migration (`db/migrations/001_create_quotes.sql`) uses `IF NOT EXISTS` and is additive-only. No destructive migrations exist. If the quotes table is corrupted:

```bash
docker compose exec postgres psql -U carrier -d carrier_gateway \
  -c "TRUNCATE quotes;"
```

The gateway continues operating without persistence if the DB is unavailable.

---

## 6. Grafana Panel Queries (Quick Reference)

| Panel                       | PromQL                                                                                   |
|-----------------------------|------------------------------------------------------------------------------------------|
| Request rate by carrier     | `sum(rate(carrier_requests_total[5m])) by (carrier_id)`                                  |
| Error rate (%)              | `sum(rate(carrier_requests_total{status!="success"}[5m])) / sum(rate(carrier_requests_total[5m])) * 100` |
| P50 latency by carrier      | `histogram_quantile(0.50, rate(carrier_quote_latency_seconds_bucket[5m])) by (carrier_id)` |
| P99 latency by carrier      | `histogram_quantile(0.99, rate(carrier_quote_latency_seconds_bucket[5m])) by (carrier_id)` |
| Circuit breaker state       | `carrier_circuit_breaker_state by (carrier_id)`                                           |
| EMA p95 latency             | `carrier_p95_latency_ms by (carrier_id)`                                                  |
| Hedge rate                  | `sum(rate(hedge_requests_total[5m])) by (trigger_carrier)`                                |
| Rate limit rejections/min   | `sum(rate(rate_limit_exceeded_total[5m])) by (carrier_id) * 60`                           |
| Fan-out P99                 | `histogram_quantile(0.99, rate(orchestrator_fan_out_duration_seconds_bucket[5m]))`         |
| CB transitions/min          | `sum(rate(circuit_breaker_transitions_total[5m])) by (carrier_id, from_state, to_state) * 60` |
