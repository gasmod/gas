# Makefile for building, testing, linting, and maintaining this Go module.
#
# Run `make` or `make help` to list the available targets. A target appears in
# the help output only if its rule line ends with a `## description` comment.
#
# The *-all targets run the matching target in every directory under this one
# that contains a Makefile (see MODS), including this directory itself.

.PHONY: help test test-coverage build lint fmt vulncheck verify mod-update \
	test-all build-all lint-all fmt-all vulncheck-all verify-all mod-update-all

# Repository root, used to locate helper scripts regardless of the directory
# make is invoked from.
REPO_ROOT := $(shell git rev-parse --show-toplevel)

# Directories containing a Makefile, relative to this one, without trailing
# slashes. Includes "." for this directory. Used by the *-all targets.
MODS := $(patsubst %/,%,$(dir $(shell find . -name Makefile -type f)))

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

##@ Bulk operations

# run_all runs `make <target>` in each directory listed in MODS.
#
# Usage: $(call run_all,<target>)
#
# Each module's output is captured and printed, indented, only if that module
# fails, so a passing run shows one status line per module. A failure does not
# stop the loop; every module is attempted, and the recipe exits non-zero if
# any of them failed.
define run_all
	@fail=0; \
	for m in $(MODS); do \
		printf '➡️  %-35s make %-6s ' "$$m" "$(1) ..."; \
		if out=$$($(MAKE) -C $$m $(1) 2>&1); then \
			echo "✅"; \
		else \
			echo "❌"; \
			printf '%s\n' "$$out" | sed 's/^/    /'; \
			fail=1; \
		fi; \
	done; \
	exit $$fail
endef

test-all: ## Run make test across all modules
	$(call run_all,test)

build-all: ## Run make build across all modules
	$(call run_all,build)

lint-all: ## Run make lint across all modules
	$(call run_all,lint)

fmt-all: ## Run make fmt across all modules
	$(call run_all,fmt)

vulncheck-all: ## Run make vulncheck across all modules
	$(call run_all,vulncheck)

verify-all: ## Run make verify across all modules
	$(call run_all,verify)

mod-update-all: ## Run make mod-update across all modules
	$(call run_all,mod-update)
