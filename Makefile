# GoClode Makefile

.PHONY: all build test clean run install dev

# Variables
BINARY_NAME=goclode
BUILD_DIR=bin
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/goclode

# Run tests
test:
	go test -v ./...

# Run with race detector
test-race:
	go test -race -v ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -rf .goclode/*.db

# Run the application
run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run in debug mode
debug: build
	./$(BUILD_DIR)/$(BINARY_NAME) --debug

# Install to GOPATH/bin
install:
	go install ./cmd/goclode

# Development mode with auto-rebuild
dev:
	@echo "Starting development mode..."
	@while true; do \
		$(MAKE) build && ./$(BUILD_DIR)/$(BINARY_NAME); \
		echo "Restarting..."; \
		sleep 1; \
	done

# Download dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	golangci-lint run

# Generate coverage report
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Check for vulnerabilities
vuln:
	@which govulncheck > /dev/null || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

# Show help
help:
	@echo "GoClode Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make build    - Build the binary"
	@echo "  make run      - Build and run"
	@echo "  make debug    - Build and run in debug mode"
	@echo "  make test     - Run tests"
	@echo "  make clean    - Clean build artifacts"
	@echo "  make install  - Install to GOPATH/bin"
	@echo "  make deps     - Download dependencies"
	@echo "  make fmt      - Format code"
	@echo "  make lint     - Lint code"
	@echo "  make coverage - Generate coverage report"
