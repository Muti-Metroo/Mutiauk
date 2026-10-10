.PHONY: all build build-linux build-windows build-windows-arm64 \
	deploy-windows-test clean test lint fmt install help \
	loadtest-up loadtest-down loadtest-shell loadtest-run loadtest-logs

# Build variables
BINARY_NAME := mutiauk
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -ldflags "-X github.com/postalsys/mutiauk/internal/cli.Version=$(VERSION) \
	-X github.com/postalsys/mutiauk/internal/cli.GitCommit=$(GIT_COMMIT) \
	-X github.com/postalsys/mutiauk/internal/cli.BuildDate=$(BUILD_DATE)"

# Go variables
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

all: build ## Build for current platform

build: ## Build for current platform
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/mutiauk

build-linux: ## Build for Linux (required for TUN support)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/mutiauk

build-linux-arm64: ## Build for Linux ARM64
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/mutiauk

# macOS builds pin the Go 1.25 toolchain: the vendored gVisor does not compile
# under Go 1.26 (pkg/sync constant redeclaration).
build-darwin: ## Build for macOS ARM64 (Apple Silicon)
	GOTOOLCHAIN=go1.25.5 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 ./cmd/mutiauk

build-darwin-amd64: ## Build for macOS AMD64 (Intel)
	GOTOOLCHAIN=go1.25.5 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 ./cmd/mutiauk

# Windows builds pin the Go 1.25 toolchain for the same gVisor reason as
# macOS. The Wintun driver DLL is embedded in the binary (see internal/tun),
# so the .exe is self-contained.
build-windows: ## Build for Windows AMD64
	GOTOOLCHAIN=go1.25.5 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe ./cmd/mutiauk

build-windows-arm64: ## Build for Windows ARM64
	GOTOOLCHAIN=go1.25.5 GOOS=windows GOARCH=arm64 go build $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-arm64.exe ./cmd/mutiauk

build-all: build-linux build-linux-arm64 build-darwin build-darwin-amd64 build-windows build-windows-arm64 ## Build for all supported platforms

# Build the Windows ARM64 binary and copy it to the test host (10.8.0.13).
# Override WIN_TEST_HOST to target a different machine.
WIN_TEST_HOST ?= 10.8.0.13
deploy-windows-test: build-windows-arm64 ## Build + scp the Windows ARM64 binary to the test host
	scp bin/$(BINARY_NAME)-windows-arm64.exe $(WIN_TEST_HOST):mutiauk.exe

clean: ## Clean build artifacts
	rm -rf bin/
	go clean

test: ## Run tests
	go test -v ./...

test-coverage: ## Run tests with coverage
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run linter
	golangci-lint run ./...

fmt: ## Format code
	go fmt ./...
	gofumpt -w .

tidy: ## Tidy dependencies
	go mod tidy

install: build-linux ## Install to /usr/local/bin
	sudo cp bin/$(BINARY_NAME)-linux-amd64 /usr/local/bin/$(BINARY_NAME)
	sudo chmod +x /usr/local/bin/$(BINARY_NAME)

# Docker targets for testing
docker-build: ## Build Docker image for testing
	docker build -t mutiauk:test -f test/Dockerfile .

docker-test: ## Run in Docker for testing
	docker-compose -f test/docker-compose.yml up --build

docker-shell: ## Start shell in test container
	docker-compose -f test/docker-compose.yml run --rm kali bash

# Development helpers
run-daemon: build-linux ## Run daemon in Docker
	docker-compose -f test/docker-compose.yml up kali

logs: ## Show daemon logs
	docker-compose -f test/docker-compose.yml logs -f kali

# Load testing with Muti Metroo tunnel
loadtest-up: build-linux ## Start load test environment
	docker-compose -f test/loadtest/docker-compose.yml up --build -d

loadtest-down: ## Stop load test environment
	docker-compose -f test/loadtest/docker-compose.yml down -v

loadtest-shell: ## Get shell in load test kali container
	docker-compose -f test/loadtest/docker-compose.yml exec kali bash

loadtest-run: ## Run full load test suite
	docker-compose -f test/loadtest/docker-compose.yml exec kali /scripts/run-tests.sh

loadtest-logs: ## Show load test logs
	docker-compose -f test/loadtest/docker-compose.yml logs -f

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
