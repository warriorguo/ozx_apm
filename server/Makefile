# OZX APM Server Makefile

# Build settings
BINARY_NAME := ozx-apm-server
BUILD_DIR := bin
CMD_DIR := cmd/server
GO := go
GOFLAGS := -v
LDFLAGS := -s -w

# Version info (can be overridden)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')

# Docker settings
DOCKER_IMAGE := ozx-apm-server
DOCKER_TAG ?= latest
DOCKER_COMPOSE_FILE := deployments/docker-compose.yml

# Test settings
COVERAGE_FILE := coverage.out
COVERAGE_HTML := coverage.html

# Build tags
BUILD_TAGS ?=

.PHONY: all build run clean test test-unit test-integration coverage lint fmt vet \
        docker-build docker-run docker-up docker-down docker-logs \
        deps tidy vendor migrate help

# Default target
all: build

## Build targets

# Build the server binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -tags "$(BUILD_TAGS)" \
		-ldflags "$(LDFLAGS) -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)" \
		-o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for Linux (for Docker or deployment)
build-linux:
	@echo "Building $(BINARY_NAME) for Linux..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -tags "$(BUILD_TAGS)" \
		-ldflags "$(LDFLAGS) -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)" \
		-o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./$(CMD_DIR)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64"

# Run the server locally
run: build
	@echo "Starting $(BINARY_NAME)..."
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run with hot reload (requires air: go install github.com/air-verse/air@latest)
dev:
	@command -v air >/dev/null 2>&1 || { echo "air not found. Install: go install github.com/air-verse/air@latest"; exit 1; }
	air

## Clean targets

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f $(COVERAGE_FILE) $(COVERAGE_HTML)
	$(GO) clean -cache -testcache

## Test targets

# Run all tests
test: test-unit

# Run unit tests only
test-unit:
	@echo "Running unit tests..."
	$(GO) test -v -race -short ./internal/...

# Run integration tests (requires ClickHouse running)
test-integration:
	@echo "Running integration tests..."
	$(GO) test -v -race ./tests/integration/...

# Run all tests with coverage
coverage:
	@echo "Running tests with coverage..."
	$(GO) test -v -race -coverprofile=$(COVERAGE_FILE) -covermode=atomic ./internal/...
	$(GO) tool cover -func=$(COVERAGE_FILE)
	@echo "Coverage report: $(COVERAGE_FILE)"

# Generate HTML coverage report
coverage-html: coverage
	$(GO) tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML)
	@echo "HTML coverage report: $(COVERAGE_HTML)"
	@command -v open >/dev/null 2>&1 && open $(COVERAGE_HTML) || true

## Code quality targets

# Run linter (requires golangci-lint)
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run ./...

# Format code
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	@command -v goimports >/dev/null 2>&1 && goimports -w . || true

# Run go vet
vet:
	@echo "Running go vet..."
	$(GO) vet ./...

# Check for security issues (requires gosec)
sec:
	@command -v gosec >/dev/null 2>&1 || { echo "gosec not found. Install: go install github.com/securego/gosec/v2/cmd/gosec@latest"; exit 1; }
	gosec -quiet ./...

## Dependency management

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GO) mod download

# Tidy go.mod
tidy:
	@echo "Tidying go.mod..."
	$(GO) mod tidy

# Vendor dependencies
vendor:
	@echo "Vendoring dependencies..."
	$(GO) mod vendor

## Docker targets

# Build Docker image (legacy, both servers)
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Build server-only image (SDK ingestion)
docker-build-server:
	@echo "Building server Docker image..."
	cd .. && docker build -f server/Dockerfile.server -t $(DOCKER_IMAGE)-server:$(DOCKER_TAG) .

# Build admin image (frontend + backend)
docker-build-admin:
	@echo "Building admin Docker image..."
	cd .. && docker build -f server/Dockerfile.admin -t $(DOCKER_IMAGE)-admin:$(DOCKER_TAG) .

# Run Docker container (standalone)
docker-run: docker-build
	docker run -p 8080:8080 -p 8081:8081 $(DOCKER_IMAGE):$(DOCKER_TAG)

# Start server services with docker-compose
docker-up-server:
	@echo "Starting server services..."
	docker-compose -f ../docker-compose.server.yml up -d

# Start admin services with docker-compose
docker-up-admin:
	@echo "Starting admin services..."
	docker-compose -f ../docker-compose.admin.yml up -d

# Start all services with docker-compose (legacy)
docker-up:
	@echo "Starting services..."
	docker-compose -f $(DOCKER_COMPOSE_FILE) up -d

# Stop server services
docker-down-server:
	@echo "Stopping server services..."
	docker-compose -f ../docker-compose.server.yml down

# Stop admin services
docker-down-admin:
	@echo "Stopping admin services..."
	docker-compose -f ../docker-compose.admin.yml down

# Stop all services
docker-down:
	@echo "Stopping services..."
	docker-compose -f $(DOCKER_COMPOSE_FILE) down

# View docker-compose logs
docker-logs:
	docker-compose -f $(DOCKER_COMPOSE_FILE) logs -f

# Rebuild and restart services
docker-rebuild:
	@echo "Rebuilding and restarting services..."
	docker-compose -f $(DOCKER_COMPOSE_FILE) up -d --build

# Clean up Docker resources
docker-clean:
	@echo "Cleaning Docker resources..."
	docker-compose -f $(DOCKER_COMPOSE_FILE) down -v --rmi local

## Database targets

# Run database migrations (via ClickHouse client)
migrate:
	@echo "Running migrations..."
	@if [ -f scripts/migrate.sql ]; then \
		docker exec -i ozx-apm-clickhouse clickhouse-client --multiquery < scripts/migrate.sql; \
	else \
		echo "No migration file found at scripts/migrate.sql"; \
	fi

# Connect to ClickHouse CLI
db-cli:
	docker exec -it ozx-apm-clickhouse clickhouse-client

## Development helpers

# Generate mocks (requires mockgen)
mocks:
	@command -v mockgen >/dev/null 2>&1 || { echo "mockgen not found. Install: go install go.uber.org/mock/mockgen@latest"; exit 1; }
	@echo "Generating mocks..."
	$(GO) generate ./...

# Check if the code compiles
check:
	@echo "Checking compilation..."
	$(GO) build -v ./...

# Run pre-commit checks
pre-commit: fmt vet lint test-unit
	@echo "Pre-commit checks passed!"

## Help

# Show this help
help:
	@echo "OZX APM Server - Available targets:"
	@echo ""
	@echo "Build & Run:"
	@echo "  make build          - Build the server binary"
	@echo "  make build-linux    - Cross-compile for Linux"
	@echo "  make run            - Build and run the server"
	@echo "  make dev            - Run with hot reload (requires air)"
	@echo "  make clean          - Remove build artifacts"
	@echo ""
	@echo "Testing:"
	@echo "  make test           - Run unit tests"
	@echo "  make test-unit      - Run unit tests only"
	@echo "  make test-integration - Run integration tests"
	@echo "  make coverage       - Run tests with coverage report"
	@echo "  make coverage-html  - Generate HTML coverage report"
	@echo ""
	@echo "Code Quality:"
	@echo "  make fmt            - Format code"
	@echo "  make vet            - Run go vet"
	@echo "  make lint           - Run golangci-lint"
	@echo "  make sec            - Run security checks"
	@echo "  make pre-commit     - Run all pre-commit checks"
	@echo ""
	@echo "Dependencies:"
	@echo "  make deps           - Download dependencies"
	@echo "  make tidy           - Tidy go.mod"
	@echo "  make vendor         - Vendor dependencies"
	@echo ""
	@echo "Docker:"
	@echo "  make docker-build        - Build Docker image (legacy)"
	@echo "  make docker-build-server - Build server image (SDK ingestion only)"
	@echo "  make docker-build-admin  - Build admin image (frontend + backend)"
	@echo "  make docker-run          - Run Docker container"
	@echo "  make docker-up-server    - Start server services"
	@echo "  make docker-up-admin     - Start admin services"
	@echo "  make docker-down-server  - Stop server services"
	@echo "  make docker-down-admin   - Stop admin services"
	@echo "  make docker-logs         - View service logs"
	@echo "  make docker-rebuild      - Rebuild and restart services"
	@echo "  make docker-clean        - Clean Docker resources"
	@echo ""
	@echo "Database:"
	@echo "  make migrate        - Run database migrations"
	@echo "  make db-cli         - Connect to ClickHouse CLI"
	@echo ""
	@echo "Development:"
	@echo "  make mocks          - Generate mocks"
	@echo "  make check          - Check compilation"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=$(VERSION)"
	@echo "  DOCKER_TAG=$(DOCKER_TAG)"
