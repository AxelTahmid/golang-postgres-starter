# Repository guidance

## Project overview

This repository is a reusable Go HTTP API template. It uses chi, PostgreSQL
through pgx, sqlc-generated queries, Goose migrations, River jobs, JWT
authentication, structured logging, Prometheus metrics, and a compiled typed
HTTP/OpenAPI layer in `internal/httpx`.

Module path: `github.com/AxelTahmid/tinker`.

The project targets Go 1.26 and `encoding/json/v2`. Commands that compile or
test the application must set `GOEXPERIMENT=jsonv2`; use the Make targets,
which do this consistently.

## Commands

- `make test` — run the full test suite.
- `make openapi` — regenerate `internal/server/openapi.json` without env, DB,
  keys, or network.
- `make openapi-gate` — verify deterministic generation and no committed-spec
  drift.
- `make sqlc-gen` — regenerate `internal/db/sqlc` after SQL changes.
- `make lint` — run golangci-lint.
- `make run` — run the API locally.
- `make init` — first-time local environment setup.

Prefer existing Make targets over one-off command variants.

## Architecture

Application code lives under `internal/`; dependency-free leaf packages that a
consumer of this template could lift wholesale live under `pkg/`.

- `cmd/api`: production server entry point.
- `cmd/openapi`: deterministic build-time OpenAPI generator.
- `config`: environment-backed runtime configuration plus canonical docs
  configuration.
- `internal/api/<domain>`: handlers, services, repositories, and request/
  response types.
- `internal/httpx`: compiled typed HTTP framework and OpenAPI generator.
- `internal/db`: database clients, migrations, queries, and generated sqlc.
- `internal/db/cache`: Postgres-backed key/value cache and the fixed-window
  rate limiter built on it.
- `internal/jwt`: JWT service, request context, and typed route guards.
- `internal/middleware`: transport-only middleware.
- `internal/server`: composition root, route tree, Scalar UI, and embedded
  OpenAPI artifact.
- `pkg/acl`: permission slugs the route guards enforce and document.
- `pkg/argon2id`, `pkg/clientip`, `pkg/filter`: password hashing, trusted
  client-address resolution, list query parsing.
- `pkg/buildinfo`, `pkg/ctxkeys`, `pkg/ptrx`, `pkg/slogx`: build metadata,
  shared context keys, pointer helpers, slog handlers.

Keep handlers thin, business rules in services, and SQL access in repositories
or generated queries. Pass `context.Context` through handler → service → DB.
Inject dependencies explicitly; do not add hidden mutable globals.

## Composition root

`server.Bootstrap` wires the database, JWT service, validators and HTTP server
once for every binary. Entry points call it and then do only their own work —
`cmd/api` starts the job queue, `cmd/openapi` writes the document. Add shared
wiring there, never in a `main`, or the entry points drift apart.

`server.RouterForDocs` compiles the same `Routes()` tree over `db.NewInert()`,
a client whose construction-time accessors succeed and whose query, transaction
and ping methods panic. Docs generation therefore runs every real service
constructor without a database. Never hand a nil dependency to a constructor to
make a build-time path work; use the inert client so a mistake fails loudly.

## Typed HTTP operations

API modules return `*httpx.Group` and must not import chi. Declare routes with
`httpx.Operation[Req, Res]`; the handler signature is the request and response
schema. `internal/httpx` is the only package that owns routing details.

Every top-level request field must have exactly one binding tag:

- `path:"id"`
- `query:"limit"`
- `header:"Idempotency-Key"`
- `cookie:"refresh_token"`
- `body:"required"` or `body:"optional"`

Do not put a body on GET or HEAD. Validation belongs in `validate:` tags and
is compiled into runtime binding and OpenAPI. Build rejects untagged fields,
unknown validation/schema tags, mismatched path parameters, invalid response
modes, undeclared enums, duplicate routes, and incomplete documentation.

Choose one explicit response mode and matching reply constructor:

| Wire shape | Declaration | Handler reply |
| --- | --- | --- |
| `{message,data}` | `httpx.Enveloped(status)` | `httpx.OK(msg, value)` |
| `{message,data,meta}` | `httpx.PageOf(status)` | `httpx.Paged(msg, rows, meta)` |
| `{message}` | `httpx.Message(status)` | `httpx.Done(msg)` |
| redirect | `httpx.RedirectWith(status)` | `httpx.RedirectTo(url)` |

A typed handler returns errors; it never writes them. Expected client failures
must use typed constructors such as `httpx.NewBadRequestError`,
`NewUnauthorizedError`, `NewForbiddenError`, `NewNotFoundError`, or
`NewConflictError`, and the operation must declare the corresponding
`Problems`. Raw errors and undeclared typed problems become a sanitized 500.
Validation and adapter errors are framework-owned and need no declaration.

Guards are sealed behavior plus documentation. Construct them with
`httpx.NewGuard`, return errors instead of writing responses, and preserve
group-before-operation ordering. Infrastructure handlers use `HandleInfra` and
are deliberately absent from OpenAPI. Use `RegisterRaw` only when the typed
response model cannot represent the wire protocol.

Register every named string enum reachable from a request or response with
`httpx.RegisterEnum`. Register custom-marshaled scalar schemas before any
route tree is built. Both registrations are part of the runtime/schema
contract.

## OpenAPI workflow

`internal/server/openapi.json` is generated from the same compiled route tree
the server runs and is embedded into the binary. Route descriptions live in
operation declarations; field descriptions and constraints live in struct
tags. Do not hand-edit the generated JSON.

After changing a route, request/response type, validation rule, guard, status,
or enum:

1. Run `make openapi`.
2. Review the generated contract diff.
3. Run `make test` and `make openapi-gate` once the generated artifact is
   tracked.

Component names and operation IDs are client-facing API. Treat renames as
coordinated breaking changes.

## Database and security

Use sqlc queries under `internal/db/queries`; do not add hand-written SQL in
handlers. Use transactions for multi-step writes and keep them short — always
through `WithTransaction`, whose named return is what makes a failed callback
roll back. Log raw database errors at the service boundary and return sanitized
typed problems. Never expose password hashes, tokens, connection strings, or
internal DB errors.

`DB_LOG_QUERIES` traces every statement with its bound arguments. It is a
development aid and must stay off in production, where it would write
credentials and personal data into the log stream.

Rate limits go through `internal/db/cache`, the one shared cache mechanism.
Its fixed-window counter is a single atomic statement, and rate-limit keys
hash the identifier so raw emails and addresses never sit in the table. Do not
add a per-feature limiter table.

Forwarding headers are accepted only through `pkg/clientip` and only from
`SERVER_TRUSTED_PROXIES`. Do not reintroduce Chi's unconditional `RealIP`
middleware or read client-controlled forwarding headers directly.

JWT guards publish claims on the request context. Derive identity from that
context instead of accepting identity fields the client can spoof. Keep auth
failures generic to avoid account enumeration. Never log request bodies in
recovery or authentication paths.

## Change discipline

- Keep changes focused and idiomatic; prefer explicit code over clever
  abstractions.
- Preserve user changes in a dirty worktree.
- Do not edit generated sqlc files by hand.
- Do not hand-edit `internal/server/openapi.json`.
- Add tests for reusable framework behavior and security-sensitive changes.
- Releases are cut with `make release` (conventional commits → svu bump →
  `git-chglog` → tag). Do not hand-edit `VERSION` or `CHANGELOG.md`.
- Do not commit unless the user explicitly requests it.
