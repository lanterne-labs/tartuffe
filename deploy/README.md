# Digital Mirror Monitoring

Prometheus + Grafana monitoring for Digital Mirror.

## Quick Start (Local Development)

### 1. Start the backend with metrics
```bash
# From repo root
./dm web serve --port 8080
```

### 2. Start Prometheus + Grafana
```bash
cd deploy
docker-compose up -d
```

### 3. Access dashboards
- **Grafana**: http://localhost:3001 (admin/admin)
- **Prometheus**: http://localhost:9090

Pre-configured dashboards:
- API Overview - request rates, latency, errors
- Database - connection pool, query latency, vector search
- LLM Operations - API calls, latency, retries
- Business Metrics - facts, contradictions, news
- System Health - goroutines, memory, GC

### 4. Stop
```bash
docker-compose down
```

---

## Production: self-hosted observability on the host

Prometheus + Grafana run colocated in a single host service (`observability`) built from `deploy/observability/Dockerfile`, supervised by s6-overlay, sharing one persistent volume at `/data`. Prometheus scrapes the `mirror` service over the host's private network; Grafana is exposed publicly on the host-issued domain.

### One-time the host setup
1. **New service** in the project: connect this repo, set Dockerfile path to `deploy/observability/Dockerfile`. Name it `observability`.
2. **Volume**: 5 GB, mount path `/data`.
3. **Env vars**:
   - `GF_SECURITY_ADMIN_PASSWORD` — admin login password
   - `GF_SERVER_ROOT_URL` — public URL of the service (set after generating the domain)
4. **Networking**: Generate a public domain. The host routes it to Grafana on port 3000 automatically (Grafana listens on `$PORT`).
5. **Cleanup on `mirror`**: remove `PROMETHEUS_REMOTE_WRITE_URL/USER/PASSWORD` env vars (no longer used).

### Service-name dependency
`deploy/observability/prometheus.yml` scrapes `mirror.host.internal:9090`. If your host service is named differently, update that file.

---

## Available Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `digital_mirror_http_requests_total` | Counter | HTTP requests by method/path/status |
| `digital_mirror_http_request_duration_seconds` | Histogram | Request latency |
| `digital_mirror_http_requests_in_flight` | Gauge | Concurrent requests |
| `digital_mirror_sse_connections_active` | Gauge | Active SSE connections |
| `digital_mirror_db_query_duration_seconds` | Histogram | DB query latency |
| `digital_mirror_db_connection_pool_total` | Gauge | Pool stats |
| `digital_mirror_db_vector_search_duration_seconds` | Histogram | pgvector latency |
| `digital_mirror_llm_requests_total` | Counter | LLM API calls |
| `digital_mirror_llm_request_duration_seconds` | Histogram | LLM latency |
| `digital_mirror_facts_extracted_total` | Counter | Facts by source/canonical |
| `digital_mirror_contradictions_detected_total` | Counter | Total contradictions |
| `digital_mirror_honesty_score_updates` | Histogram | Score distribution |

---

## Testing Metrics Locally

```bash
# Check metrics are exposed
curl http://localhost:8080/metrics | grep digital_mirror

# Example output
digital_mirror_http_requests_total{method="GET",path="/api/persons",status="200"} 5
digital_mirror_db_connection_pool_total{state="idle"} 2
```
