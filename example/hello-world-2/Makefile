.PHONY: help test test-coverage build lint fmt

# Default target
help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Testing
test: ## Run tests
	go test -race ./...

test-coverage: ## Run tests with coverage
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Building
build: ## Build the binary
	go build -o build/hello-world-2 ./cmd

# Linting and formatting
lint: ## Run linters
	golangci-lint run

fmt: ## Format code
	golangci-lint fmt

vet: ## Run go vet
	go vet ./...
