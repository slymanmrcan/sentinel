# Container metrics

Sentinel keeps container telemetry optional and disabled by default. When
enabled, the dashboard reads the Docker Engine API every 15 seconds and shows
running container CPU, memory working set, network counters and PID count.

## Recommended setup

Expose only the required read-only Docker API routes through an authenticated,
network-restricted proxy and point Sentinel at it:

```env
CONTAINER_METRICS_ENABLED=true
CONTAINER_API_URL=http://docker-metrics-proxy:2375
```

The proxy needs these routes:

- `GET /containers/json?all=false`
- `GET /containers/{id}/stats?stream=false`

Do not expose an unauthenticated Docker TCP endpoint. Anyone who can control the
Docker daemon can normally obtain host-level control.

## Direct Unix socket mode

For a trusted single-host lab, Sentinel can connect directly to a Unix socket:

```env
CONTAINER_METRICS_ENABLED=true
CONTAINER_API_URL=
DOCKER_SOCKET=/var/run/docker.sock
```

When Sentinel itself runs in Docker, this additionally requires a manual
read-only-looking bind mount such as
`/var/run/docker.sock:/var/run/docker.sock`. The `:ro` mount flag does not make
the Docker API read-only: API calls can still mutate the daemon. For that
reason, the repository's default Compose file never mounts this socket.

For an explicitly trusted Linux host, the repository includes an opt-in
override:

```bash
docker compose -f docker-compose.yml -f docker-compose.containers.yml up -d --build
```

After startup, verify the two required flags without printing the full Compose
environment, which may contain secrets:

```bash
docker compose -f docker-compose.yml -f docker-compose.containers.yml exec sentinel \
  sh -c 'test "$CONTAINER_METRICS_ENABLED" = true && test -S "$DOCKER_SOCKET"'
docker compose -f docker-compose.yml -f docker-compose.containers.yml logs --tail=100 sentinel
```

## Metric semantics

- CPU uses Docker's current versus previous CPU counter delta and online CPU
  count. Sentinel deliberately waits for Docker's two-point non-streaming sample;
  a one-shot response has no usable previous counter. A multi-core container can
  therefore exceed 100%.
- Memory subtracts `inactive_file` on cgroup v2 or
  `total_inactive_file` on cgroup v1, matching Docker CLI working-set display.
- Network RX/TX are cumulative counters summed across the container's network
  interfaces, not live rates or ISP billing usage.
- Containers are sorted by CPU and collection is capped at 64 running
  containers per Sentinel instance. Stats calls use bounded concurrency so the
  two-point CPU sample does not add one serial wait per container.

If the daemon cannot be reached, the UI says unavailable instead of displaying
zero-valued measurements.
