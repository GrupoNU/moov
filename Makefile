# Moov Mail — developer and CI entry points.
#
# Portability: this Makefile is run both from Git Bash on Windows and from
# Linux CI, so every recipe is POSIX sh. No bashisms, no `find -printf`, no
# GNU-only flags, and no assumptions about the shell being bash.

SHELL := /bin/sh

# ---------------------------------------------------------------------------
# Build metadata, stamped into internal/version at link time.
# ---------------------------------------------------------------------------
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

VERSION_PKG := github.com/GrupoNU/moov/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) \
           -X $(VERSION_PKG).Commit=$(COMMIT) \
           -X $(VERSION_PKG).Date=$(DATE)

BIN_DIR := bin
GO      ?= go

# `go run` for the pinned tools, so a contributor needs no extra install step
# and CI cannot drift from local. golangci-lint is the exception: it is
# installed by its official action in CI and by `make lint-install` locally,
# because running it through `go run` pulls a very large dependency tree into
# this module.
GOLANGCI ?= golangci-lint

.DEFAULT_GOAL := help
.PHONY: help build run test test-short cover fmt fmt-check vet lint lint-install \
        corpus-check db-up db-down db-logs migrate tidy clean ci

help: ## Show this help
	@grep -h -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
	  | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Build & run
# ---------------------------------------------------------------------------
build: ## Build moovd into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/...

run: ## Run moovd from source
	$(GO) run -ldflags "$(LDFLAGS)" ./cmd/moovd

# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------
test: ## Run all tests (store tests need MOOV_TEST_DATABASE_URL)
	$(GO) test -race -count=1 ./...

test-short: ## Run tests without the ones requiring external services
	$(GO) test -short -count=1 ./...

cover: ## Run tests with a coverage profile
	$(GO) test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -1

# ---------------------------------------------------------------------------
# Static checks
# ---------------------------------------------------------------------------
fmt: ## Format the code
	$(GO) fmt ./...

# gofmt -l prints the files that need formatting. `test -z` turns a non-empty
# list into a failure, which is what CI needs; the list is printed first so the
# failure says which files.
fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l . 2>/dev/null | grep -v '^vendor/' | grep -v '^spikes/' || true); \
	if [ -n "$$out" ]; then \
	  echo "These files are not gofmt-clean:"; \
	  echo "$$out"; \
	  echo "Run: make fmt"; \
	  exit 1; \
	fi
	@echo "gofmt: clean"

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint
	$(GOLANGCI) run ./...

# Keep this version in step with .github/workflows/ci.yml.
GOLANGCI_VERSION ?= v2.1.6

lint-install: ## Install golangci-lint locally (same version as CI)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

# ---------------------------------------------------------------------------
# Corpus
# ---------------------------------------------------------------------------
corpus-check: ## Validate the MIME corpus against its manifest
	$(GO) run ./tools/corpuscheck

# ---------------------------------------------------------------------------
# Development database
# ---------------------------------------------------------------------------
COMPOSE ?= docker compose -f docker-compose.dev.yml

db-up: ## Start the development PostgreSQL 17 and wait for it
	$(COMPOSE) up -d --wait

db-down: ## Stop the development database (keeps the volume)
	$(COMPOSE) down

db-reset: ## Stop the development database and DELETE its data
	$(COMPOSE) down -v

db-logs: ## Follow the development database logs
	$(COMPOSE) logs -f postgres

migrate: ## Apply migrations to the development database
	$(GO) run ./tools/migrate

# ---------------------------------------------------------------------------
# Housekeeping
# ---------------------------------------------------------------------------
tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

# ---------------------------------------------------------------------------
# The full local gate. Matches what CI runs, so a green `make ci` means a green
# pipeline — minus the jobs that need service containers.
# ---------------------------------------------------------------------------
ci: fmt-check vet lint build corpus-check test ## Run the full local gate
	@echo "ci: all checks passed"
