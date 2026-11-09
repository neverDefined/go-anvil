.PHONY: help test test-coverage bench lint fmt vet check install-tools check-foundry

.DEFAULT_GOAL := help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

test: ## Run tests with the race detector
	go test -race -shuffle=on ./...

test-coverage: ## Write an HTML coverage report to coverage.html
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html

bench: ## Run benchmarks
	go test -run '^$$' -bench=. -benchmem ./...

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format code
	go fmt ./...
	@which goimports > /dev/null && goimports -w . || echo "goimports not installed, skipping"

vet: ## Run go vet
	go vet ./...

check: fmt vet lint test ## Run fmt, vet, lint and test

install-tools: ## Install golangci-lint and goimports
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b $$(go env GOPATH)/bin
	go install golang.org/x/tools/cmd/goimports@latest

check-foundry: ## Check that anvil is installed
	@which anvil > /dev/null || (echo "anvil not found. Install Foundry: https://book.getfoundry.sh/getting-started/installation" && exit 1)
	@anvil --version
