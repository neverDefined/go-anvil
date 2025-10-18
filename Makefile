.PHONY: help test test-verbose test-coverage lint fmt clean install-tools build deps check

# Default target
.DEFAULT_GOAL := help

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt

help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

test: ## Run tests
	$(GOTEST) -v -race ./...

test-verbose: ## Run tests with verbose output
	$(GOTEST) -v -race -count=1 ./...

test-coverage: ## Run tests with coverage report
	$(GOTEST) -v -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

test-short: ## Run short tests only
	$(GOTEST) -v -short ./...

bench: ## Run benchmarks
	$(GOTEST) -bench=. -benchmem ./...

lint: ## Run linters
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run 'make install-tools' first." && exit 1)
	golangci-lint run --timeout=5m

lint-fix: ## Run linters and fix issues
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Run 'make install-tools' first." && exit 1)
	golangci-lint run --fix --timeout=5m

fmt: ## Format code
	$(GOFMT) ./...
	@which goimports > /dev/null && goimports -w . || echo "goimports not installed, skipping"

vet: ## Run go vet
	$(GOCMD) vet ./...

build: ## Build the package
	$(GOBUILD) -v ./...

clean: ## Clean build artifacts
	$(GOCLEAN)
	rm -f coverage.out coverage.html

deps: ## Download dependencies
	$(GOMOD) download
	$(GOMOD) tidy

deps-upgrade: ## Upgrade dependencies
	$(GOGET) -u ./...
	$(GOMOD) tidy

deps-verify: ## Verify dependencies
	$(GOMOD) verify

install-tools: ## Install development tools
	@echo "Installing golangci-lint..."
	@which golangci-lint > /dev/null || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin
	@echo "Installing goimports..."
	@which goimports > /dev/null || $(GOGET) golang.org/x/tools/cmd/goimports@latest
	@echo "Tools installed successfully!"

check: fmt vet lint test ## Run all checks (format, vet, lint, test)

ci: deps check ## Run CI pipeline locally

verify: ## Verify the project is ready for commit
	@echo "Running verification checks..."
	@$(MAKE) fmt
	@$(MAKE) vet
	@$(MAKE) lint
	@$(MAKE) test
	@echo "✓ All checks passed!"

doc: ## Generate and serve documentation
	@echo "Starting documentation server at http://localhost:6060"
	@echo "Visit http://localhost:6060/pkg/github.com/neverDefined/go-anvil/"
	godoc -http=:6060

check-foundry: ## Check if Foundry is installed
	@which anvil > /dev/null || (echo "Foundry not installed. Visit https://book.getfoundry.sh/getting-started/installation" && exit 1)
	@anvil --version
	@echo "✓ Foundry is installed"

.PHONY: all
all: deps check ## Run deps and check

