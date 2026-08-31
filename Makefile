.PHONY: help test test-coverage build lint fmt

MODS := $(patsubst %/,%,$(dir $(shell find . -name Makefile -type f)))

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
	go build ./...

# Linting and formatting
lint: ## Run linters
	golangci-lint run

fmt: ## Format code
	golangci-lint fmt

vet: ## Run go vet
	go vet ./...

# Module Update

mod-update: ## Update go modules
	go mod tidy
	go get -u all
	go mod tidy

####################
#     Bulk Ops     #
####################

define run_all
	@fail=0; \
	for m in $(MODS); do \
		printf '➡️  %-20s make %-6s ... ' "$$m" "$(1)"; \
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

vet-all: ## Run make vet across all modules
	$(call run_all,vet)

mod-update-all: ## Run mod-update across all modules
	$(call run_all,mod-update)
