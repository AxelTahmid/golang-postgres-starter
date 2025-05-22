# Improved Makefile with best practices

# PHONY targets - all non-file targets should be declared here.
.PHONY: help check-env tls jwt deps deps-upgrade deps-cleancache tidy run routes build up down fresh init dev exec-db log log-db sqlc-gen sqlc-vet \
        migrate-status migrate-up migrate-down migrate-dto migrate-redo migrate-fresh migrate-validate migrate-version migrate-create \
        river-up river-down lint vet clean gen 

# Variables for common commands
# GOOSE and RIVER doesnt provide docker images. SQLC does.
GOOSE := docker compose --profile tools run --rm goose
RIVER := docker compose --profile tools run --rm river
SQLC := docker run --rm --network tinker-api_tinker_network -v $(PWD):/src -w /src --user $(shell id -u):$(shell id -g) sqlc/sqlc
# Golint should be installed in machine for IDE integration. Depreciated.
# GOLINT := docker run -t --rm -v $(PWD):/app -w /app golangci/golangci-lint:latest-alpine golangci-lint

# Load .env file if it exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Default target: display help
help:
	@echo "Available targets:"
	@echo "  help              - Display this help message"
	@echo "  check-env         - Ensure .env exists (copy from .env.example if missing)"
	@echo "  tls               - Generate self-signed TLS certificates for local development"
	@echo "  jwt               - Generate JWT keys"
	@echo "  deps              - Download and verify Go modules"
	@echo "  deps-upgrade      - Upgrade Go module dependencies"
	@echo "  deps-cleancache   - Clean Go module cache"
	@echo "  tidy              - Tidy Go modules"
	@echo "  run               - Run the API server"
	@echo "  routes            - Run the routes server"
	@echo "  build             - Build the API server binary (requires os and arch variables)"
	@echo "  gen               - Run code generation"
	@echo "  up                - Start Docker containers"
	@echo "  down              - Stop Docker containers"
	@echo "  fresh             - Rebuild and restart Docker containers (no cache)"
	@echo "  init              - Initialize environment, generate certs, and run migrations"
	@echo "  dev               - Prepare the environment and start development mode"
	@echo "  exec-db           - Open a shell inside the database container"
	@echo "  log               - Follow logs for the API container"
	@echo "  log-db            - Follow logs for the database container"
	@echo "  swagger           - Start swagger ui container at 3002"
	@echo "  sqlc-gen          - Generate SQL code using sqlc"
	@echo "  migrate-status    - Show migration status"
	@echo "  migrate-up        - Migrate database to latest version"
	@echo "  migrate-down      - Roll back the last migration"
	@echo "  migrate-dto       - Migrate down to version 18"
	@echo "  migrate-redo      - Re-run the latest migration"
	@echo "  migrate-fresh     - Reset all migrations"
	@echo "  migrate-validate  - Validate migration files"
	@echo "  migrate-version   - Show current migration version"
	@echo "  migrate-create    - Create a new migration file (requires filename variable)"
	@echo "  river-up          - Run river migrate-up"
	@echo "  river-down        - Run river migrate-down"
	@echo "  lint              - Run golangci-lint"
	@echo "  vet               - Run Go vet via golangci-lint"
	@echo "  clean             - Remove built binaries and temporary files"

# ----------------------------------------------------------------------
# Environment Setup
# ----------------------------------------------------------------------

# Ensure .env exists; if not, copy from .env.example
check-env:
	@test -f .env || cp .env.example .env

# ----------------------------------------------------------------------
# Certificate and Key Generation
# ----------------------------------------------------------------------

# Generate self-signed TLS certificates (local development only)
tls:
	@echo "Generating TLS certificates..."
	cd cert && \
	openssl req -nodes -newkey rsa:2048 -new -x509 -keyout tls.key -out tls.crt -days 365 \
	-subj "/C=BD/ST=Dhaka/L=Dhaka/O=Golang/CN=localhost"

# Generate JWT keys
jwt:
	@echo "Generating JWT keys..."
	cd cert && \
	openssl ecparam -genkey -name prime256v1 -noout -out jwt-pvt.pem && \
	openssl ec -in jwt-pvt.pem -pubout -out jwt-pub.pem

# ----------------------------------------------------------------------
# Dependency Management
# ----------------------------------------------------------------------

deps:
	go mod download
	go mod verify

deps-upgrade:
	go get -u -t -d -v ./...

deps-cleancache:
	go clean -modcache

tidy:
	go mod tidy

# ----------------------------------------------------------------------
# Application Commands
# ----------------------------------------------------------------------

# Run the API server
run:
	go run ./cmd/api/main.go

# Run the routes server
routes:
	go run ./cmd/routes/main.go

# Build the API server binary (requires os and arch variables)
build:
	@if [ -z "$(os)" ] || [ -z "$(arch)" ]; then \
		echo "Error: Both 'os' and 'arch' variables must be set. Usage: make build os=<value> arch=<value>"; \
		exit 1; \
	fi
	CGO_ENABLED=0 GOOS=$(os) GOARCH=$(arch) go build -x -o ./bin/main ./cmd/api/main.go

gen:
	go generate ./...

# ----------------------------------------------------------------------
# Docker Management
# ----------------------------------------------------------------------

up:
	docker compose up -d

down:
	docker compose down

fresh: check-env
	docker compose down --remove-orphans
	docker compose build --no-cache
	docker compose up -d --build -V
	log

init:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/riverqueue/river/cmd/river@latest
	$(MAKE) check-env
	$(MAKE) tls
	$(MAKE) jwt
	docker compose down --remove-orphans
	docker compose build --no-cache
	docker compose up -d --build -V
	$(MAKE) migrate-up
	$(MAKE) river-up
	$(MAKE) log

# Development mode: tidy modules, restart containers, and follow logs
dev: tidy down up log

# Open a shell in the database container
exec-db:
	docker compose exec -it db sh

# Follow logs for API and database containers
log:
	docker logs -f api

log-db:
	docker logs -f db

swagger:
	docker compose --profile tools up -d swagger
# ----------------------------------------------------------------------
# SQL Code Generation
# ----------------------------------------------------------------------

sqlc-gen:
	$(SQLC) generate

sqlc-vet:
	$(SQLC) vet

# ----------------------------------------------------------------------
# Database Migrations using Goose
# ----------------------------------------------------------------------

migrate-status:
	$(GOOSE) status

migrate-up:
	$(GOOSE) up

migrate-down:
	$(GOOSE) down

migrate-dto:
	$(GOOSE) down-to 8

migrate-redo:
	$(GOOSE) redo

migrate-fresh:
	$(GOOSE) reset

migrate-validate:
	$(GOOSE) validate

migrate-version:
	$(GOOSE) version

# Create a new migration file (requires filename variable)
migrate-create:
	@if [ -z "$(filename)" ]; then \
		echo "Error: 'filename' variable must be set. Usage: make migrate-create filename=<value>"; \
		exit 1; \
	fi
	$(GOOSE) create $(filename) sql

# ----------------------------------------------------------------------
# Database Migrations using River CLI
# ----------------------------------------------------------------------

river-up:
	$(RIVER) migrate-up

river-down:
	$(RIVER) migrate-down

river-get:
	$(RIVER) migrate-get

river-list:
	$(RIVER) migrate-list

# ----------------------------------------------------------------------
# Code Quality
# ----------------------------------------------------------------------

# Run golangci-lint using Docker container
lint:
	golangci-lint run

# Run Go vet using golangci-lint (without a config)
vet:
	golangci-lint run --no-config --enable govet

# ----------------------------------------------------------------------
# Cleanup
# ----------------------------------------------------------------------

clean:
	rm -rf ./bin
