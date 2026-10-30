# Makefile for building, testing, linting, and maintaining this Go module.
#
# Run `make` or `make help` to list the available targets. A target appears in
# the help output only if its rule line ends with a `## description` comment.

.PHONY: help test test-coverage build lint fmt vulncheck verify mod-update

# Repository root, used to locate helper scripts regardless of the directory
# make is invoked from.
REPO_ROOT := $(shell git rev-parse --show-toplevel)

##@ General

# help is the default target because it is the first rule in the file.
help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

##@ Testing

test: ## Run tests with the race detector inside the isolated test container
	$(REPO_ROOT)/scripts/docker-test.sh go test -race ./...

# Coverage is collected across all packages (-coverpkg=./...), so code
# exercised indirectly by another package's tests is counted too.
# Writes test/coverage.out (raw profile) and test/coverage.html (report).
test-coverage: ## Run tests with coverage and generate an HTML report
	go test -v -race -coverpkg=./... -coverprofile=test/coverage.out ./...
	go tool cover -html=test/coverage.out -o test/coverage.html

##@ Building

# Compiles every package to check that the module builds. With multiple
# packages, `go build ./...` discards the results rather than writing binaries.
build: ## Compile all packages
	go build ./...

##@ Code quality

# `go fix -diff` prints suggested fixes without applying them.
lint: ## Run golangci-lint and show pending go fix changes
	golangci-lint run
	go fix -diff ./...

fmt: ## Format code with the formatters configured in golangci-lint
	golangci-lint fmt
	golangci-lint run --fix ./...
	go fix ./...

vulncheck: ## Scan dependencies for known vulnerabilities
	govulncheck ./...

verify: vulncheck lint test build ## Run lint, test, and build (pre-push check)

##@ Dependencies

# Tidies before and after the upgrade so the update starts from a clean
# go.mod and leaves no unused requirements behind.
mod-update: ## Upgrade all dependencies to their latest minor/patch versions
	go mod tidy
	go get -u all
	go mod tidy
