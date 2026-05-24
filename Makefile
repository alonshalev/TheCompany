.PHONY: build run test migrate dev docker-up docker-down lint fmt web-dev web-build web-install

BINARY_NAME=symbiont
BUILD_DIR=./bin
CMD_DIR=./cmd/symbiont

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

# Run in development mode (uses .env)
run: build
	$(BUILD_DIR)/$(BINARY_NAME) serve

# Run all tests
test:
	go test -v -race ./...

# Run migrations up
migrate-up:
	go run $(CMD_DIR) migrate up

# Run migrations down (one step)
migrate-down:
	go run $(CMD_DIR) migrate down

# Start Docker Compose (Postgres + pgvector)
docker-up:
	docker compose -f deployments/docker/docker-compose.yml up -d

# Stop Docker Compose
docker-down:
	docker compose -f deployments/docker/docker-compose.yml down

# Full local dev: docker up + run
dev: docker-up
	@sleep 2
	go run $(CMD_DIR) serve

# Lint
lint:
	golangci-lint run ./...

# Format
fmt:
	gofmt -w .
	goimports -w .

# Generate mocks (requires mockgen)
generate:
	go generate ./...

# Tidy dependencies
tidy:
	go mod tidy

# ── React frontend targets ────────────────────────────────────

WEB_DIR=./web

# Install frontend dependencies
web-install:
	cd $(WEB_DIR) && npm install

# Start Vite dev server (proxies /v1 to localhost:8080)
web-dev:
	cd $(WEB_DIR) && npm run dev

# Production build — outputs to web/dist/
web-build:
	cd $(WEB_DIR) && npm run build

# Full local dev: docker + Go server + Vite dev server (in parallel)
dev-full: docker-up
	@sleep 2
	@echo "Starting Go server and Vite dev server in parallel..."
	@trap 'kill 0' INT; \
		go run $(CMD_DIR) serve & \
		cd $(WEB_DIR) && npm run dev & \
		wait

help:
	@echo "Symbiont — available make targets:"
	@echo "  build        Build the binary"
	@echo "  run          Build and run the server"
	@echo "  test         Run all tests"
	@echo "  migrate-up   Apply pending migrations"
	@echo "  migrate-down Roll back last migration"
	@echo "  docker-up    Start Postgres + pgvector"
	@echo "  docker-down  Stop Postgres"
	@echo "  dev          Full local dev (docker + server)"
	@echo "  lint         Run golangci-lint"
	@echo "  fmt          Format all Go files"
	@echo "  tidy         Tidy go.mod"
	@echo ""
	@echo "Frontend targets:"
	@echo "  web-install  npm install in web/"
	@echo "  web-dev      Start Vite dev server (port 5173)"
	@echo "  web-build    Production build → web/dist/"
	@echo "  dev-full     docker + Go server + Vite in parallel"
