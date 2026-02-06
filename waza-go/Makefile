# Waza-Go Build System
.PHONY: all build clean test lint fmt install help

# Build configuration
BINARY_NAME=waza
BUILD_DIR=.
GO_FILES=$(shell find . -name '*.go' -not -path './vendor/*')
VERSION?=0.1.0
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

# Default target
all: fmt lint test build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/waza

# Run tests
test:
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

# Run linter
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping..."; \
		echo "Install: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin v1.64.8"; \
	fi

# Format code
fmt:
	@echo "Formatting code..."
	@gofmt -w $(GO_FILES)
	@go mod tidy

# Install binary to GOPATH
install:
	@echo "Installing $(BINARY_NAME)..."
	@go install $(LDFLAGS) ./cmd/waza

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -f $(BUILD_DIR)/$(BINARY_NAME)
	@rm -f coverage.out
	@go clean -cache -testcache

# Show help
help:
	@echo "Waza-Go Makefile Targets:"
	@echo "  all      - Format, lint, test, and build (default)"
	@echo "  build    - Compile the binary"
	@echo "  test     - Run all tests with coverage"
	@echo "  lint     - Run golangci-lint"
	@echo "  fmt      - Format Go code and tidy modules"
	@echo "  install  - Install binary to GOPATH"
	@echo "  clean    - Remove build artifacts"
	@echo "  help     - Show this help message"
