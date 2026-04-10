.PHONY: build run clean download-ollama build-all test lint help

# Default target
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build for current OS/arch
	@bash scripts/build.sh current

build-all: ## Build for all platforms
	@bash scripts/build.sh all

run: ## Build and run the agent
	go run .

run-debug: ## Run with debug logging
	LOG_LEVEL=debug go run .

download-ollama: ## Download Ollama binary for current platform
	@bash scripts/download-ollama.sh

clean: ## Remove build artifacts
	rm -rf bin/ dist/

test: ## Run tests
	go test ./... -v

lint: ## Run linter (requires golangci-lint)
	golangci-lint run ./...

dev: download-ollama run ## Download Ollama + run agent
