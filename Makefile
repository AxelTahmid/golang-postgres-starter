.PHONY: build init checkenv

# Load all values from .env and export them, within makefile commands
# ifneq (,$(wildcard ./.env))
#     include .env
#     export
# endif

checkenv:
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
	fi

# self-seigned tls for local dev only
tls:
	cd ./cert && \
	openssl req -nodes -newkey rsa:2048 -new -x509 -keyout tls.key -out tls.crt -days 365 \
	-subj "//C=BD/ST=Dhaka/L=Dhaka/O=Golang/CN=localhost"

jwt:
	cd ./cert && \
	openssl ecparam -genkey -name prime256v1 -noout -out jwt-pvt.pem && \
	openssl ec -in jwt-pvt.pem -pubout -out jwt-pub.pem

deps:
	go mod download
	go mod verify

deps-upgrade: 
	go get -u -t -d -v ./...

deps-cleancache: 
	go clean -modcache

tidy:
	go mod tidy

run: 
	go run ./cmd/api/main.go

routes: 
	go run ./cmd/routes/main.go

build: 
	@if [ -z "$(os)" ] || [ -z "$(arch)" ]; then \
		echo "Error: Both 'os' and 'arch' variables must be set. Please use 'make build os=<value> arch=<value>'"; \
		exit 1; \
	fi
	CGO_ENABLED=0 GOOS=$(os) GOARCH=$(arch) go build -x -o ./bin/main ./cmd/api/main.go

up:
	docker compose up -d

down:
	docker compose down

fresh:
	checkenv
	docker compose down --remove-orphans
	docker compose build --no-cache
	docker compose up -d --build -V
	make log

init:
	checkenv
	make tls
	make jwt
	docker compose down --remove-orphans
	docker compose build --no-cache
	docker compose up -d --build -V
	make migrate-up
	make log

dev: tidy down up log

exec-db: 
	docker exec -it db sh

log:
	docker logs -f api

log-db: 
	docker logs -f db

sqlc=docker run --rm -v $(PWD):/src -w /src --user $(shell id -u):$(shell id -g) sqlc/sqlc:1.27.0
sqlc-gen:
	${sqlc} generate
	
## Database migration scripts
goose=docker compose --profile tools run --rm goose 

migrate-status: 
	${goose} status

# Migrate the DB to the most recent version available
migrate-up: 
	${goose} up

# Roll back the version by 1
migrate-down: 
	${goose} down

migrate-dto: 
	${goose} down-to 13

# Re-run the latest migration
migrate-redo: 
	${goose} redo

# Roll back all migrations
migrate-fresh: 
	${goose} reset

# Check migration files without running them
migrate-validate: 
	${goose} validate

migrate-version: 
	${goose} version

# Creates new migration file with the current sequence 
migrate-create:
	@if [ -z "$(filename)" ]; then \
		echo "Error: 'filename' variable must be set. Please use 'make migrate-create filename=<value>'"; \
		exit 1; \
	fi
	${goose} create $(filename) sql

golangci=docker run -t --rm -v $(PWD):/app -w /app golangci/golangci-lint:latest-alpine golangci-lint
lint: 
	${golangci} run

vet: 
	${golangci} run  --no-config --enable govet