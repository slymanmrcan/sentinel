.PHONY: all build run clean docker-build docker-up docker-down docker-logs fmt vet lint vulncheck check

BINARY_NAME=sentinel
DB_FILE=metrics.db

all: check build

build:
	@echo "Building local Go binary..."
	go build -o $(BINARY_NAME) .

run: build
	@echo "Running sentinel locally on port 8000..."
	./$(BINARY_NAME)

clean:
	@echo "Cleaning up build artifacts..."
	rm -f $(BINARY_NAME)
	# Optionally comment out the DB delete if you want persistence
	# rm -f $(DB_FILE)

fmt:
	@echo "Formatting Go code..."
	go fmt ./...

vet:
	@echo "Vetting Go code..."
	go vet ./...

lint: fmt vet
	@echo "Running golangci-lint..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint is not installed. You can install it from https://golangci-lint.run/"; \
		echo "Skipping golangci-lint check."; \
	fi

vulncheck:
	@echo "Running vulnerability check (govulncheck)..."
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt vet lint vulncheck
	@echo "All quality checks completed successfully!"

docker-build:
	@echo "Building Docker image..."
	docker compose build

docker-up:
	@echo "Starting sentinel in Docker..."
	docker compose up -d

docker-down:
	@echo "Stopping sentinel in Docker..."
	docker compose down

docker-logs:
	@echo "Showing docker logs..."
	docker compose logs -f
