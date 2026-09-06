# Container metrics

Sentinel keeps container telemetry optional and disabled by default. When
enabled, the dashboard reads the Docker Engine API every 30 seconds and shows
all container states, plus running container CPU, memory working set, network counters and PID count.
The initial interval can be configured with
`CONTAINER_COLLECTION_INTERVAL=30s`; supported values are `15s`, `30s`, `45s`,
`1m`, and `2m`.

## Recommended setup

Expose only the required read-only Docker API routes through an authenticated,
network-restricted proxy and point Sentinel at it:

```env
CONTAINER_METRICS_ENABLED=true
CONTAINER_API_URL=http://docker-metrics-proxy:2375
```

The proxy needs these routes:

- `GET /containers/json?all=true`
- `GET /containers/{id}/stats?stream=false&one-shot=true`

Do not expose an unauthenticated Docker TCP endpoint. Anyone who can control the
Docker daemon can normally obtain host-level control.

## Direct Unix socket mode

For a trusted single-host lab, Sentinel can connect directly to a Unix socket:

```env
CONTAINER_METRICS_ENABLED=true
CONTAINER_API_URL=
DOCKER_SOCKET=/var/run/docker.sock
```

The current Compose file already mounts
`/var/run/docker.sock:/var/run/docker.sock`. The `:ro` mount flag does not make
the Docker API read-only. Remove the socket mount if you switch to a restricted
HTTP proxy. No additional Compose override is shipped.

## Metric semantics

- CPU uses Docker's current versus previous CPU counter delta and online CPU
  count. Sentinel takes fast one-shot Docker samples and keeps the previous
  counter itself, avoiding Docker's per-container two-point wait. CPU is shown as unavailable
  for the first collection after Sentinel starts and becomes an interval value
  on the next collection. A multi-core container can exceed 100%.
- Memory subtracts `inactive_file` on cgroup v2 or
  `total_inactive_file` on cgroup v1, matching Docker CLI working-set display.
- Network RX/TX are cumulative counters summed across the container's network
  interfaces, not live rates or ISP billing usage.
- Container state remains visible for the full returned inventory. Stats
  collection is capped at 64 running containers; skipped stats are marked unavailable. One-shot stats calls use bounded
  concurrency so hosts with many containers stay within the collection timeout.

If the daemon cannot be reached, the UI says unavailable instead of displaying
zero-valued measurements.

## Runtime interval

The authenticated header selector updates the real backend collection timer,
not only browser polling. The choice is persisted in DuckDB and takes precedence
over `CONTAINER_COLLECTION_INTERVAL` after restart. Changing the interval is a
CSRF-protected write and creates a monitor event in the system event stream.

Stopped containers remain listed without stats calls. A stats failure on one
container does not discard other readings. Incomplete aggregate totals are shown
as unavailable. No containers found is not labelled healthy. Stale snapshots are
marked unavailable after max(90 seconds, 2 × collection interval + 30 seconds).
