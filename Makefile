# Load .env when present.
ifneq (,$(wildcard ./.env))
	include .env
	export
endif

GOOSE := docker compose --profile tools run --rm goose
RIVER := docker compose --profile tools run --rm river

.PHONY: help
## help: Display this help message
help:
	@echo "Usage:"
	@echo "  make <target> [variables]"
	@echo ""
	@echo "Available targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

# ----------------------------------------------------------------------
# Environment setup
# ----------------------------------------------------------------------

.PHONY: check-env
## check-env: Ensure .env exists; copy .env.example when it does not
check-env:
	@test -f .env || cp .env.example .env

.PHONY: tls jwt
## tls: Generate a self-signed TLS certificate for local development
tls:
	@echo "Generating TLS certificates..."
	@mkdir -p cert
	@openssl req -nodes -newkey rsa:2048 -new -x509 \
		-keyout cert/tls.key -out cert/tls.crt -days 365 \
		-subj "/C=BD/ST=Dhaka/L=Dhaka/O=Golang/CN=localhost"

## jwt: Generate the local ECDSA JWT key pair
jwt:
	@echo "Generating JWT keys..."
	@mkdir -p cert
	@openssl ecparam -genkey -name prime256v1 -noout -out cert/jwt-pvt.pem
	@openssl ec -in cert/jwt-pvt.pem -pubout -out cert/jwt-pub.pem

# ----------------------------------------------------------------------
# Dependencies and application commands
# ----------------------------------------------------------------------

.PHONY: deps deps-upgrade deps-cleancache tidy
## deps: Download and verify Go modules
deps:
	@go mod download
	@go mod verify

## deps-upgrade: Upgrade direct and test dependencies
deps-upgrade:
	@go get -u -t ./...

## deps-cleancache: Clear the Go module cache
deps-cleancache:
	@go clean -modcache

## tidy: Tidy Go module declarations
tidy:
	@GOEXPERIMENT=jsonv2 go mod tidy

.PHONY: run build gen
## run: Run the API server
run:
	GOEXPERIMENT=jsonv2 go run ./cmd/api

## build: Build the API binary (make build os=linux arch=amd64)
build:
	@if [ -z "$(os)" ] || [ -z "$(arch)" ]; then \
		echo "Error: os and arch are required. Usage: make build os=<value> arch=<value>"; \
		exit 1; \
	fi
	GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=$(os) GOARCH=$(arch) \
		go build -trimpath -o ./bin/main ./cmd/api

## gen: Run Go code generation
gen:
	GOEXPERIMENT=jsonv2 go generate ./...

# ----------------------------------------------------------------------
# Docker development environment
# ----------------------------------------------------------------------

.PHONY: up down fresh init dev exec-db log log-db
## up: Start development containers
up:
	@docker compose up -d

## down: Stop development and tool containers
down:
	@docker compose down
	@docker compose --profile tools down --remove-orphans

## fresh: Rebuild and restart containers without cache
fresh: check-env
	@docker compose down --remove-orphans
	@docker compose build --no-cache
	@docker compose up -d --build -V
	$(MAKE) log

## init: Install tools and initialize the local development environment
init:
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@go install github.com/pressly/goose/v3/cmd/goose@latest
	@go install github.com/riverqueue/river/cmd/river@latest
	@go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	$(MAKE) check-env
	$(MAKE) tls
	$(MAKE) jwt
	@docker compose down --remove-orphans
	@docker compose build --no-cache
	@docker compose up -d --build -V
	$(MAKE) migrate-up
	$(MAKE) river-up
	$(MAKE) log

## dev: Tidy dependencies, restart containers, and follow API logs
dev: tidy down up log

## exec-db: Open a shell in the database container
exec-db:
	@docker compose exec -it database sh

## log: Follow API container logs
log:
	docker logs -f api

## log-db: Follow database container logs
log-db:
	docker logs -f db

# ----------------------------------------------------------------------
# API documentation
# ----------------------------------------------------------------------

.PHONY: openapi openapi-gate
## openapi: Generate the OpenAPI 3.1 document from compiled typed routes
openapi:
	GOEXPERIMENT=jsonv2 go run ./cmd/openapi

## openapi-gate: Verify deterministic generation and committed-spec drift
openapi-gate:
	@./scripts/openapi-drift-gate.sh

# ----------------------------------------------------------------------
# SQL code and migrations
# ----------------------------------------------------------------------

.PHONY: sqlc-gen sqlc-vet
## sqlc-gen: Generate type-safe database code with sqlc
sqlc-gen:
	@sqlc generate

## sqlc-vet: Vet sqlc queries
sqlc-vet:
	@sqlc vet

.PHONY: migrate-status migrate-up migrate-down migrate-redo migrate-fresh migrate-validate migrate-version migrate-create
## migrate-status: Show Goose migration status
migrate-status:
	$(GOOSE) status

## migrate-up: Apply all pending database migrations
migrate-up:
	$(GOOSE) up

## migrate-down: Roll back the latest database migration
migrate-down:
	$(GOOSE) down

## migrate-redo: Roll back and reapply the latest migration
migrate-redo:
	$(GOOSE) redo

## migrate-fresh: Reset all database migrations
migrate-fresh:
	$(GOOSE) reset

## migrate-validate: Validate database migration files
migrate-validate:
	$(GOOSE) validate

## migrate-version: Show the current database migration version
migrate-version:
	$(GOOSE) version

## migrate-create: Create a SQL migration (make migrate-create filename=add_widgets)
migrate-create:
	@if [ -z "$(filename)" ]; then \
		echo "Error: filename is required. Usage: make migrate-create filename=<value>"; \
		exit 1; \
	fi
	$(GOOSE) create $(filename) sql

.PHONY: river-up river-down river-get river-list
## river-up: Apply River queue migrations
river-up:
	$(RIVER) migrate-up

## river-down: Roll back the latest River queue migration
river-down:
	$(RIVER) migrate-down

## river-get: Show River queue migration state
river-get:
	$(RIVER) migrate-get

## river-list: List River queue migrations
river-list:
	$(RIVER) migrate-list

# ----------------------------------------------------------------------
# Tests and code quality
# ----------------------------------------------------------------------

.PHONY: test test-httpx lint vet
## test: Run the complete Go test suite
test:
	GOEXPERIMENT=jsonv2 go test ./...

## test-httpx: Run the typed HTTP/OpenAPI framework tests
test-httpx:
	GOEXPERIMENT=jsonv2 go test -v ./internal/httpx ./cmd/openapi ./internal/server

## lint: Run golangci-lint
lint:
	GOEXPERIMENT=jsonv2 golangci-lint run

## vet: Run Go vet
vet:
	GOEXPERIMENT=jsonv2 go vet ./...

.PHONY: clean
## clean: Remove built binaries
clean:
	rm -rf ./bin
