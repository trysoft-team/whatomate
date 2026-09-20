.PHONY: all build build-prod run test clean docker-build docker-up docker-down migrate frontend-dev frontend-build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
BINARY_NAME=whatomate
BINARY_PATH=./cmd/whatomate
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"
# Local host runs use config.local.toml (localhost:5433 Postgres / localhost Redis).
# Override: make run CONFIG=config.toml
CONFIG?=config.local.toml

# Docker parameters
DOCKER_COMPOSE=docker compose

all: build

# Build the backend (development - without frontend)
build:
	$(GOBUILD) -o $(BINARY_NAME) $(BINARY_PATH)

# Build production binary with embedded frontend
build-prod: frontend-build embed-frontend
	CGO_ENABLED=0 $(GOBUILD) $(LDFLAGS) -o $(BINARY_NAME) $(BINARY_PATH)
	@echo "Production binary built: $(BINARY_NAME)"
	@echo "Version: $(VERSION)"
	@ls -lh $(BINARY_NAME)

# Copy frontend build to embed directory
embed-frontend:
	@echo "Copying frontend build to embed directory..."
	@rm -rf internal/frontend/dist/*
	@cp -r frontend/dist/* internal/frontend/dist/
	@echo "Frontend embedded successfully"

# Run the backend locally
run:
	@test -f $(CONFIG) || (echo "Missing $(CONFIG). Copy config.toml → config.local.toml and set database.host=localhost port=5433, redis.host=localhost"; exit 1)
	$(GOCMD) run $(BINARY_PATH)/main.go server -config $(CONFIG)

# Run with migrations
run-migrate:
	@test -f $(CONFIG) || (echo "Missing $(CONFIG). Copy config.toml → config.local.toml and set database.host=localhost port=5433, redis.host=localhost"; exit 1)
	$(GOCMD) run $(BINARY_PATH)/main.go server -config $(CONFIG) -migrate

# Run tests. Uses gotestsum when available for live progress + a clear
# failure summary at the end. Falls back to the built-in `go test -v` so
# nothing breaks for devs who haven't installed it.
# Install:  go install gotest.tools/gotestsum@latest
test:
	@if command -v gotestsum >/dev/null 2>&1; then \
		gotestsum --format testname --hide-summary=skipped -- ./...; \
	else \
		echo "(install gotestsum for nicer output: go install gotest.tools/gotestsum@latest)"; \
		$(GOTEST) -v ./...; \
	fi

# Run tests with coverage. Same gotestsum fallback as `make test`.
test-coverage:
	@if command -v gotestsum >/dev/null 2>&1; then \
		gotestsum --format testname --hide-summary=skipped -- -coverprofile=coverage.out ./...; \
	else \
		echo "(install gotestsum for nicer output: go install gotest.tools/gotestsum@latest)"; \
		$(GOTEST) -v -coverprofile=coverage.out ./...; \
	fi
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html

# Download dependencies
deps:
	$(GOMOD) download
	$(GOMOD) tidy

# Update dependencies
deps-update:
	$(GOMOD) tidy
	$(GOGET) -u ./...

# Docker commands
docker-build:
	$(DOCKER_COMPOSE) build

docker-up:
	$(DOCKER_COMPOSE) up -d

docker-down:
	$(DOCKER_COMPOSE) down

docker-logs:
	$(DOCKER_COMPOSE) logs -f

docker-restart:
	$(DOCKER_COMPOSE) restart

# Database migrations
migrate:
	@test -f $(CONFIG) || (echo "Missing $(CONFIG). Copy config.toml → config.local.toml and set database.host=localhost port=5433, redis.host=localhost"; exit 1)
	$(GOCMD) run $(BINARY_PATH)/main.go server -config $(CONFIG) -migrate

# Infra only (Postgres + Redis). App runs on the host via make run-migrate / make frontend-dev.
docker-infra:
	$(DOCKER_COMPOSE) up -d db redis
	@$(DOCKER_COMPOSE) stop app 2>/dev/null || true
	@echo "db (localhost:5433) and redis (localhost:6379) are up; app container stopped."

# Frontend commands
frontend-install:
	cd frontend && npm install

frontend-dev:
	@if [ ! -d "frontend/node_modules" ]; then \
		echo "Installing frontend dependencies..."; \
		cd frontend && npm install; \
	fi
	cd frontend && npm run dev

frontend-build:
	@if [ ! -d "frontend/node_modules" ]; then \
		echo "Installing frontend dependencies..."; \
		cd frontend && npm install; \
	fi
	cd frontend && npm run build

frontend-preview:
	cd frontend && npm run preview

# Development - run both backend and frontend (requires docker-infra first)
dev:
	@echo "Starting backend ($(CONFIG)) and frontend..."
	@echo "API :8080  |  UI :3000 (proxies /api → :8080)"
	@make run-migrate &
	@make frontend-dev

# Lint
lint:
	golangci-lint run ./...

# Format code
fmt:
	$(GOCMD) fmt ./...

# Generate swagger docs (if using)
swagger:
	swag init -g cmd/whatomate/main.go -o api/docs

# Help
help:
	@echo "Available targets:"
	@echo ""
	@echo "Production:"
	@echo "  build-prod     - Build single binary with embedded frontend"
	@echo ""
	@echo "Development (host app + Docker db/redis):"
	@echo "  docker-infra   - Start Postgres+Redis only; stop app container"
	@echo "  run            - Run the backend locally (CONFIG=$(CONFIG))"
	@echo "  run-migrate    - Run the backend with database migrations"
	@echo "  frontend-dev   - Run Vite UI on :3000"
	@echo "  dev            - Run backend + frontend together"
	@echo ""
	@echo "Frontend:"
	@echo "  frontend-install - Install frontend dependencies"
	@echo "  frontend-build - Build frontend for production"
	@echo ""
	@echo "Testing:"
	@echo "  test           - Run tests"
	@echo "  test-coverage  - Run tests with coverage report"
	@echo ""
	@echo "Docker (full stack):"
	@echo "  docker-build   - Build Docker images"
	@echo "  docker-up      - Start all Docker containers"
	@echo "  docker-down    - Stop Docker containers"
	@echo "  docker-logs    - View Docker logs"
	@echo ""
	@echo "Other:"
	@echo "  clean          - Remove build artifacts"
	@echo "  deps           - Download dependencies"
	@echo "  lint           - Run linter"
	@echo "  fmt            - Format code"
