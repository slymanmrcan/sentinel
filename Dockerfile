# Build stage
FROM golang:1.25.12-bookworm AS builder
WORKDIR /app

# Copy dependency manifests
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build the API entrypoint
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o sentinel ./cmd/api

# Run stage
FROM debian:12-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/sentinel /app/sentinel

# Expose HTTP port
EXPOSE 8000

# Environment variables
ENV PORT=8000
ENV DB_PATH=/data/metrics.db

# Persistent directory volume
VOLUME /data

HEALTHCHECK --interval=30s --timeout=4s --start-period=15s --retries=3 CMD ["/app/sentinel", "healthcheck"]

CMD ["/app/sentinel"]
