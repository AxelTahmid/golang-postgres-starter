FROM golang:1.26-trixie AS base
FROM gcr.io/distroless/static-debian12 AS prod

## Dev Image, local files are mounted in volume
FROM base AS dev
WORKDIR /app
RUN go install github.com/air-verse/air@latest
COPY go.mod go.sum ./
RUN go mod download
CMD ["air", "-c", ".air.toml"]

FROM base AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN GOEXPERIMENT=jsonv2 CGO_ENABLED=0 GOOS=linux go build -trimpath -o ./bin/main ./cmd/api/main.go

## Production image built using multi-step docker, slow on pipeline
FROM prod AS releasev0
WORKDIR /app
COPY --from=builder ./bin/main /
CMD ["/main"]

## Production image built using makefile command at readme
FROM prod AS release
WORKDIR /app
COPY ./bin/main /
CMD ["/main"]
# ex: docker build -t gostarter:latest --target prod .

## Goose  Migrations
FROM base AS goose
RUN go install github.com/pressly/goose/v3/cmd/goose@latest
WORKDIR /app

## River migrations for queue
FROM base AS river
RUN go install github.com/riverqueue/river/cmd/river@latest
WORKDIR /app

## Migrations Runner
FROM base AS migrations
RUN go install github.com/pressly/goose/v3/cmd/goose@latest
RUN go install github.com/riverqueue/river/cmd/river@latest
WORKDIR /app
ENV GOOSE_MIGRATION_DIR=/migrations
ENV GOOSE_DRIVER=postgres
# GOOSE_DBSTRING add on mounting
COPY ./internal/db/migrations/ /migrations
ENTRYPOINT ["goose", "-s", "-timeout", "5m"]
