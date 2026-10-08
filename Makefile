.PHONY: build test lint run clean install-tools help dev install-dev-tools

# Default target
.DEFAULT_GOAL := help

# Build variables
BINARY_NAME=msgraph-mcp-server
BUILD_DIR=bin
MAIN_PATH=cmd/server/main.go

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the server binary
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

test: ## Run all tests with race detector and coverage
	@echo "Running tests..."
	go test -v -race -cover -coverprofile=coverage.out ./...

test-tags: ## Verify the devconsole build also compiles and vets
	go build -tags devconsole ./...
	go vet -tags devconsole ./...

test-short: ## Run tests without integration tests
	@echo "Running short tests..."
	go test -v -short -race ./...

coverage: test ## Generate and display test coverage report
	@echo "Generating coverage report..."
	go tool cover -html=coverage.out

lint: ## Run linter (requires golangci-lint)
	@echo "Running linter..."
	golangci-lint run

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...
	gofmt -s -w .

vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

run: ## Run the server locally
	@echo "Starting server..."
	go run $(MAIN_PATH)

run-console: ## Run the server with the browser test console at /test (developer machines only)
	@echo "Starting server with the development console enabled..."
	go run -tags devconsole $(MAIN_PATH)

dev: ## Run the server with hot reload (requires air)
	@echo "Starting server with hot reload..."
	@if ! command -v air > /dev/null; then \
		echo "Error: air is not installed. Run 'make install-dev-tools' first."; \
		exit 1; \
	fi
	air

clean: ## Clean build artifacts and cache
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	go clean -cache -testcache
	rm -f coverage.out

deps: ## Download and tidy dependencies
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

install-tools: ## Install development tools
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/vektra/mockery/v2@latest

install-dev-tools: ## Install hot reload tool (air)
	@echo "Installing air for hot reload..."
	go install github.com/cosmtrek/air@latest
	@echo "✓ air installed. You can now use 'make dev' for hot reload."

mock: ## Generate mocks for testing
	@echo "Generating mocks..."
	mockery --all --output=test/mocks --case=underscore

docker-build: ## Build Docker image
	docker build -t $(BINARY_NAME):latest .

docker-run: ## Run Docker container
	docker run -p 8080:8080 --env-file .env $(BINARY_NAME):latest
