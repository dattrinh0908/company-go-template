# Makefile for the order service.
#
# `make help` lists every target. Targets that need a tool install it on demand
# into GOBIN, so a fresh checkout needs nothing but Go and Docker.

BINARY      := main.exe
MAIN        := ./cmd/api
MIGRATIONS  := ./migrations
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# Loaded so migrate/psql targets can reach the same database the app uses.
ifneq (,$(wildcard .env))
include .env
export
endif

DB_USERNAME ?= $(BLUEPRINT_DB_USERNAME)
DB_PASSWORD ?= $(BLUEPRINT_DB_PASSWORD)
DB_HOST     ?= $(BLUEPRINT_DB_HOST)
DB_PORT     ?= $(BLUEPRINT_DB_PORT)
DB_DATABASE ?= $(BLUEPRINT_DB_DATABASE)
DATABASE_URL ?= postgres://$(DB_USERNAME):$(DB_PASSWORD)@$(DB_HOST):$(DB_PORT)/$(DB_DATABASE)?sslmode=disable

.DEFAULT_GOAL := help

# --- development ---------------------------------------------------------

.PHONY: all
all: tidy fmt vet lint test build ## Run the full local pipeline

.PHONY: build
build: ## Compile the API binary
	@echo "Building $(VERSION)..."
	@go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) $(MAIN)

.PHONY: run
run: ## Run the API from source
	@go run $(MAIN)

.PHONY: watch
watch: ## Live reload with air
	@powershell -ExecutionPolicy Bypass -Command "if (Get-Command air -ErrorAction SilentlyContinue) { \
		air; \
	} else { \
		Write-Output 'Installing air...'; \
		go install github.com/air-verse/air@latest; \
		air; \
	}"

.PHONY: clean
clean: ## Remove build artefacts and coverage output
	@echo "Cleaning..."
	@rm -f $(BINARY) coverage.out coverage.html

# --- quality -------------------------------------------------------------

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	@go mod tidy

.PHONY: fmt
fmt: ## Format the code
	@go fmt ./...

.PHONY: vet
vet: ## Run go vet
	@go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	@powershell -ExecutionPolicy Bypass -Command "if (-not (Get-Command golangci-lint -ErrorAction SilentlyContinue)) { \
		Write-Output 'Installing golangci-lint...'; \
		go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest; \
	}; golangci-lint run"

.PHONY: lint-fix
lint-fix: ## Apply every auto-fixable lint finding
	@golangci-lint run --fix

# --- tests ---------------------------------------------------------------

# Packages that need neither Docker nor a C toolchain.
UNIT_PKGS := ./internal/config/... ./internal/domain/... ./internal/transport/...

.PHONY: test
test: ## Run unit tests (no Docker required)
	@echo "Running unit tests..."
	@go test $(UNIT_PKGS) -count=1

.PHONY: test-race
test-race: ## Run unit tests under the race detector (needs CGO and a C toolchain)
	@echo "Running unit tests with -race..."
	@CGO_ENABLED=1 go test $(UNIT_PKGS) -race -count=1

.PHONY: itest
itest: ## Run repository integration tests (requires Docker)
	@echo "Running integration tests..."
	@go test ./internal/repository/... -v -count=1

.PHONY: test-all
test-all: ## Run every test
	@go test ./... -count=1

.PHONY: cover
cover: ## Produce an HTML coverage report over the unit-tested packages
	@go test $(UNIT_PKGS) -coverprofile=coverage.out
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Wrote coverage.html"

# --- database ------------------------------------------------------------

.PHONY: migrate-up
migrate-up: ## Apply every .up.sql migration
	@powershell -ExecutionPolicy Bypass -Command "if (-not (Get-Command migrate -ErrorAction SilentlyContinue)) { \
		Write-Output 'Installing golang-migrate...'; \
		go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest; \
	}; migrate -path $(MIGRATIONS) -database '$(DATABASE_URL)' up"

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	@migrate -path $(MIGRATIONS) -database '$(DATABASE_URL)' down 1

.PHONY: migrate-create
migrate-create: ## Scaffold a migration pair: make migrate-create name=add_x
	@migrate create -ext sql -dir $(MIGRATIONS) -seq $(name)

# --- containers ----------------------------------------------------------

.PHONY: docker-run
docker-run: ## Start the service and its database
	@docker compose up --build

.PHONY: docker-down
docker-down: ## Stop the compose stack
	@docker compose down

.PHONY: docker-build
docker-build: ## Build the production image
	@docker build --build-arg VERSION=$(VERSION) -t order-service:$(VERSION) .

# --- meta ----------------------------------------------------------------

.PHONY: help
help: ## List every target
	@powershell -ExecutionPolicy Bypass -Command "Select-String -Path Makefile -Pattern '^[a-zA-Z_-]+:.*?## .*$$' | ForEach-Object { \
		$$parts = $$_.Line -split ':.*?## '; \
		'{0,-16} {1}' -f $$parts[0], $$parts[1] \
	}"
