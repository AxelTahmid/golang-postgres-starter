FROM golang:1.23-bookworm as base
FROM gcr.io/distroless/static-debian12 as prod

## Dev Runner, local files are volume mounted
FROM base as dev
WORKDIR /app
RUN go install github.com/air-verse/air@latest
COPY go.mod go.sum ./
RUN go mod download
CMD ["air", "-c", ".air.toml"]

## Production Runner, build using makefile command at readme
FROM prod as release
WORKDIR /app
RUN mkdir /app/cert
COPY ./bin/main /
CMD ["/main"]
# ex: docker build -t gostarter:latest --target prod .

## Goose  Migrations
FROM base as goose
RUN go install github.com/pressly/goose/v3/cmd/goose@latest
WORKDIR /app


