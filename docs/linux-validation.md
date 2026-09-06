# Linux validation checklist

Use this checklist on the actual Linux host before treating the dashboard as
production-verified. macOS unit tests and a successful image build cannot prove
host `/proc`, `/sys`, cgroup or Docker daemon behavior.

## Static checks

```bash
go test -race ./...
go vet ./...
go build ./cmd/api
docker compose config --quiet
docker build -t sentinel:validation .
```

## Host telemetry comparison

Run Sentinel for at least two collection intervals, then compare:

```bash
top -b -n 2 -d 10
free -b
df -B1 /
cat /proc/loadavg
ip -s link
cat /proc/net/dev
```

For the repository's Compose setup, compare the explicit host namespace with
the caller's namespace inside Sentinel:

```bash
docker compose exec sentinel sh -c 'readlink /host/proc/net; cat /host/proc/1/net/dev; cat /host/proc/net/dev'
```

`/host/proc/net` follows `self/net` and may describe Sentinel's container network.
The collector uses `/host/proc/1/net/dev` and `/host/proc/1/net/tcp{,6}` instead.
The PID 1 paths must be readable; missing host data is reported as unavailable.
Process ownership of listening ports may remain unknown when FD permissions
prevent attribution. Native installations without `HOST_PROC` use OS readers.

Expect small timing differences: the tools do not sample at exactly the same
instant. Confirm that network interface selection matches
`NETWORK_INTERFACES`; otherwise bridges, loopback and veth devices may be
included.

## Container telemetry comparison

With container metrics explicitly enabled:

```bash
docker stats --no-stream
docker inspect --format '{{.State.Pid}}' CONTAINER
```

Check CPU, working-set memory and PID count for multiple idle and busy
containers. Docker network figures and Sentinel network figures are cumulative
RX/TX counters; `docker stats` formats the same counters for display.

Finally validate login, refresh, password change, logout, container API failure,
and service restart through the real HTTPS reverse proxy. Record the host OS,
kernel, cgroup version, Docker version and validation date with the result.

## Regression checks included in the repository

`go test -race ./...` covers stopped container visibility, partial Docker stats,
the 64-container stats budget, partial metric persistence/baselines, independent
alert evaluation, failed history writes, stale host samples, system-details
caching, and host-network fixtures. `node --test tests/dashboard.test.cjs` checks
stale/empty/partial dashboard states and missing-value chart gaps.

On 2026-09-06 these checks were exercised locally on macOS arm64 and in a Linux
arm64 Go 1.25.12 container. The Linux run also enabled
`SENTINEL_LINUX_TEST=1` with `--pid host` and a separate container network to
verify that the actual collector distinguishes host and container interfaces.
These are local validation results; the live server still needs the comparisons
above after deployment.
