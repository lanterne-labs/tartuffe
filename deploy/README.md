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

## The host Deployment

### Option 1: Use Grafana Cloud (Recommended - Free Tier)

The simplest approach - no extra services to deploy:

1. Sign up at https://grafana.com/products/cloud/ (free tier: 10k metrics)

2. Get your Prometheus remote write URL from Grafana Cloud

3. Add to your backend `.env`:
   ```
   PROMETHEUS_REMOTE_WRITE_URL=https://prometheus-xxx.grafana.net/api/prom/push
   PROMETHEUS_REMOTE_WRITE_USER=your-user-id
   PROMETHEUS_REMOTE_WRITE_PASSWORD=your-api-key
   ```

4. The metrics will be pushed to Grafana Cloud - import the dashboards from `grafana/dashboards/*.json`

### Option 2: Self-hosted on the host

Deploy 3 services:

#### Backend Service
Already deployed. Just ensure `/metrics` endpoint is accessible.

#### Prometheus Service
1. Create new host service
2. Point to `deploy/prometheus` directory
3. Set internal networking to connect to backend

#### Grafana Service
1. Create new host service
2. Point to `deploy/grafana` directory
3. Set `GF_SECURITY_ADMIN_PASSWORD` env var
4. Expose port 3000

Update `prometheus/prometheus.yml` with your host service URLs:
```yaml
static_configs:
  - targets: ['your-backend-service.host.internal:8080']
```

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
