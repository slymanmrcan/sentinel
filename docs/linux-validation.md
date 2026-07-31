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
