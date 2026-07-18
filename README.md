# Sentinel

Sentinel is a lightweight, zero-dependency, self-hosted system monitor built in Go. It collects realtime system metrics (CPU, RAM, Disk, Uptime) and stores them in an embedded, high-performance **DuckDB** database, serving a beautiful embedded dark-mode glassmorphism dashboard.

---

## Features

- **DuckDB Powered**: High-performance embedded query engine for fast metrics and log filtering.
- **Embedded Web UI**: Single compiled binary includes the entire UI dashboard (Vanilla HTML/CSS/JS + Chart.js).
- **Security First**: Supports optional Basic Authentication for dashboard & API endpoints.
- **Docker-Optimized**: Designed to run inside Docker while safely mounting and monitoring host system metrics.
- **Graceful Shutdown**: Intercepts OS termination signals (`SIGINT`, `SIGTERM`) to cleanly close DuckDB database files, preventing filesystem corruption.
- **Request Logging & Healthchecks**: Standard API metrics logging and `/healthz` live check.

---

## Getting Started

### Prerequisites

- Go 1.25.12+ (Toolchain auto-updates via `go.mod`)
- Docker & Docker Compose (optional for containerized deployment)

### Local Development

Use the included **Makefile** for common dev operations:

```bash
# Run all quality checks (formatting, vet, lint, vulncheck)
make check

# Build the local binary
make build

# Run sentinel locally on port 8000
make run

# Clean up binaries and logs
make clean
```

Once running, access the dashboard at: `http://localhost:8000`.

---

## Docker Deployment

To deploy Sentinel completely inside Docker attached to an internal bridge network (`infra_net`):

1. Ensure the network `infra_net` is created on your host:
   ```bash
   docker network create infra_net
   ```
2. Launch the services using Docker Compose:
   ```bash
   make docker-up
   # or: docker compose up -d
   ```

### Host Monitoring configuration
In `docker-compose.yml`, host directories are mounted read-only to allow the container to monitor host system stats:
- `/proc:/host/proc:ro`
- `/sys:/host/sys:ro`
- `/:/host/root:ro`

*Note: On macOS, Docker Desktop runs inside a Linux VM, so gopsutil will report VM stats. When deployed natively on Linux servers, it will report host machine statistics.*

---

## Security (Basic Authentication)

To prevent unauthorized access, you can enable Basic Auth by defining environment variables in `docker-compose.yml` or your local env:

```yaml
environment:
  - AUTH_USER=admin
  - AUTH_PASSWORD=your-super-secure-password
```

If these are set, the UI dashboard and `/api/*` endpoints will immediately require credentials. The `/healthz` endpoint remains unauthenticated for container health checking.

---

## API Documentation

- **`GET /healthz`**: Unauthenticated endpoint returning `200 OK` (used for Docker/Kubernetes health probes).
- **`GET /api/metrics/realtime`**: Returns a JSON representation of current CPU, Memory, Disk, Uptime, OS, and Hostname details.
- **`GET /api/metrics/history?range=1h`**: Returns a JSON array of historical metrics. Supported ranges: `1h` (raw data), `24h` (aggregated by 5-minute intervals).
- **`GET /api/logs?level=ALL&query=`**: Returns logs stored in DuckDB filtered by level (`INFO`, `WARN`, `ERROR`) and text search queries.
- **`POST /api/logs`**: Allows external services to write logs into Sentinel's database. Example payload:
  ```json
  {
    "level": "INFO",
    "message": "Backup completed successfully",
    "source": "backup_cron"
  }
  ```
- **`DELETE /api/logs`**: Clears all logs in the database.
