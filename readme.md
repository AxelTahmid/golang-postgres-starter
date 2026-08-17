# Go PostgreSQL API Starter

A production-minded monolithic HTTP API template with typed routes, PostgreSQL,
background jobs, JWT authentication, structured observability, and an OpenAPI
3.1 document generated from the same compiled contracts used at runtime.

## Included

- Go 1.26 with `encoding/json/v2`
- chi routing and middleware
- pgx connection pools and sqlc-generated queries
- Goose database migrations
- River PostgreSQL-backed jobs
- JWT access and refresh tokens
- validator-based request validation
- RFC 9457 problem responses
- slog structured logging and Prometheus metrics
- deterministic OpenAPI generation with a CI drift gate
- embedded Scalar API reference UI
- Air hot reload and Docker Compose development services

## Prerequisites

- Go 1.26+
- Docker and Docker Compose v2
- OpenSSL
- Make

The HTTP framework uses `encoding/json/v2`; the supplied Make and Air commands
set `GOEXPERIMENT=jsonv2` consistently.

## Quick start

For a first-time local setup:

```sh
make init
```

This creates `.env` from `.env.example`, generates local TLS/JWT keys, starts
the containers, applies Goose and River migrations, and follows API logs.

For later sessions:

```sh
make dev
```

To run the API directly on the host after its dependencies are available:

```sh
make run
```

The default endpoints are:

- `https://localhost:3000/api/v1/auth/*` — starter authentication API
- `https://localhost:3000/docs/ui` — Scalar API reference
- `https://localhost:3000/docs/json` — embedded OpenAPI 3.1 document
- `https://localhost:3000/metrics` — Prometheus metrics
- `https://localhost:3000/ping` — transport heartbeat

## Common commands

```sh
make test             # all Go tests
make lint             # golangci-lint
make openapi          # regenerate the committed API contract
make openapi-gate     # deterministic generation + drift check
make sqlc-gen         # regenerate sqlc code
make migrate-up       # apply database migrations
make migrate-down     # roll back one migration
make migrate-create filename=add_widgets
make river-up         # apply River migrations
make build os=linux arch=amd64
```

## Typed HTTP framework

`internal/httpx` compiles each endpoint declaration once into request binding,
validation, guard, response, problem, routing, and OpenAPI plans. Runtime and
documentation therefore consume the same immutable contract.

An API module returns an `*httpx.Group`; it does not import chi:

```go
func (h *Handler) Routes() *httpx.Group {
    group := httpx.NewGroup(httpx.Defaults{Tags: []string{"widget"}})

    httpx.Register(group, httpx.Operation[CreateWidgetInput, Widget]{
        Method:             http.MethodPost,
        Path:               "/",
        Summary:            "Create a widget",
        BodyDescription:    "Widget creation data",
        SuccessDescription: "Widget created",
        Success:            httpx.Enveloped(http.StatusCreated),
        Problems: []httpx.ProblemKind{
            httpx.Conflict().Described("A widget with this name already exists"),
        },
        Handler: h.Create,
    })
    return group
}
```

The request type declares where each value comes from:

```go
type CreateWidgetInput struct {
    Body CreateWidgetRequest `body:"required"`
}

type UpdateWidgetInput struct {
    ID   int64               `path:"id" doc:"Widget ID"`
    Body UpdateWidgetRequest `body:"required"`
}
```

Supported source tags are `path`, `query`, `header`, `cookie`, and `body`.
Validation tags drive both request validation and schema constraints. Typed
handlers return `*httpx.Reply[T]` and an error; expected errors use typed
problem constructors and must be declared by the operation.

Response modes are explicit:

| Mode | Wire shape | Reply |
| --- | --- | --- |
| `httpx.Enveloped(status)` | `{message,data}` | `httpx.OK` |
| `httpx.PageOf(status)` | `{message,data,meta}` | `httpx.Paged` |
| `httpx.Message(status)` | `{message}` | `httpx.Done` |
| `httpx.RedirectWith(status)` | redirect | `httpx.RedirectTo` |

`Build()` aggregates declaration errors before the server starts. It rejects
ambiguous routes, bad source tags, unmatched path placeholders, invalid
response modes, unknown validations, incomplete raw contracts, undeclared
enums, and schema collisions.

## OpenAPI and Scalar

The generated document lives at `internal/server/openapi.json` and is embedded
into the API binary. Generation is hermetic: it builds the real route tree over
inert handler dependencies, so it needs no `.env`, database, JWT keys, or
network. Scalar renders that embedded document at `/docs/ui`.

```sh
make openapi
git diff -- internal/server/openapi.json
make openapi-gate
```

Do not edit the JSON by hand. A route, validation, response, guard, enum, or
status change is an API-contract change and should regenerate the document.

## Project layout

```text
cmd/
  api/                 API server entry point
  openapi/             deterministic contract generator
config/                runtime and docs configuration
internal/
  api/                 domain handlers, services, repositories, DTOs
  clientip/            trusted-proxy-aware client address resolution
  db/                  pgx, migrations, queries, generated sqlc
  httpx/               typed route compiler and OpenAPI generator
  jwt/                 token service and typed guards
  middleware/          transport middleware
  server/              composition root, Scalar UI, embedded contract
scripts/               repository gates and helper scripts
```

## Adding an endpoint

1. Define request and response types.
2. Write a context-aware handler returning `*httpx.Reply[T]`.
3. Register an `httpx.Operation` with summary, success mode, problems, and
   guards.
4. Mount the module group in `internal/server/routes.go`.
5. Run `make openapi` and review the contract diff.
6. Run `make test`.

See [AGENTS.md](AGENTS.md) for repository-specific implementation rules.
