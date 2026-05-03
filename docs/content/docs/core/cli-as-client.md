---
title: CLI as a REST client
---

## Overview

Anna's CLI is intentionally a thin REST client. The running `anna serve`
process is the **only** thing that opens the SQLite database, writes to the
markdown library, fetches RSS feeds, or makes any other state change.

This is the project's **API-first** principle: every feature is reachable via
HTTP first, and the CLI is just one of several clients (CLI, web UI, future
SDKs and integrations all consume the same contract).

```
┌──────────┐         HTTP          ┌──────────────────────┐
│   CLI    │ ────────────────────▶ │   anna server        │
│ (anna …) │  Bearer ANNA_TOKEN    │  • SQLite            │
└──────────┘                       │  • markdown library  │
                                   │  • RSS fetchers      │
┌──────────┐                       │  • scheduler         │
│   Web    │ ────────────────────▶ │  • plugin host       │
└──────────┘                       └──────────────────────┘
┌──────────┐
│   SDK    │ ────────────────────▶
└──────────┘
```

## Why

- **One source of truth.** Business rules live in the server, not duplicated
  across CLI/Web/SDK. Two DB writers race on schema changes; one writer
  cannot.
- **Remote use works.** `ANNA_SERVER_URL=https://anna.example.com anna recally
  list` is the same code path as the local case.
- **Auditability.** Every mutation flows through HTTP, so logging, metrics,
  rate limiting, and authorization happen in one place.
- **Type safety.** The OpenAPI spec is the contract. Drift between server
  interface and client is caught at codegen / build time, not at runtime.

## Pattern

Each domain (recally is the first; scheduler, skills, tools follow) ships:

1. An OpenAPI 3.0 spec at `api/<domain>.openapi.yaml`. **This is the source of
   truth.** Code is regenerated from the spec, never the other way around.
2. A generated server interface in `internal/admin/<domain>_gen.go` and
   handlers in `internal/admin/<domain>_handlers.go` that implement it.
3. A generated client in `pkg/<domain>/client/client_gen.go`, plus a small
   `auth.go` wrapper that reads `ANNA_TOKEN` / `ANNA_SERVER_URL` from the
   environment.
4. CLI subcommands under `cmd/anna/<domain>*.go` that build a typed request
   from flags, call the generated client, decode JSON, and render output.

Regeneration is wired through mise:

```bash
mise run generate:api
```

## Adding a new domain

To add a new domain (say `notes`):

1. Write `api/notes.openapi.yaml`. Pick canonical REST URLs (`/api/notes`,
   `/api/notes/{id}`); reuse the existing `Error` response shape.
2. Add the codegen invocation to `mise run generate:api` (in `mise.toml`).
3. Run `mise run generate:api` to produce
   `internal/admin/notes_gen.go` and `pkg/notes/client/client_gen.go`.
4. Implement the generated `ServerInterface` in
   `internal/admin/notes_handlers.go`.
5. Wire it from `internal/admin/routes.go`:
   `s.registerNotesRoutes()` → `HandlerFromMux(s.notes, s.mux)`.
6. Inject any new domain stores in `internal/admin/server.go`.
7. Replace direct DB calls in `cmd/anna/notes*.go` with calls to
   `notesclient.NewFromEnv()`.

## Bearer token authentication

The CLI reads `ANNA_TOKEN` and sends it as `Authorization: Bearer …`. The
server's existing `authMiddleware` (`internal/admin/middleware.go`) already
handles bearer tokens via `authInfoFromBearer`, so new domain routes get auth
for free as soon as they are registered on `s.mux`.

Bearer token auth requires `ANNA_VAULT_KEY` to be set on the server (token
material is stored encrypted via the vault).

## What CLI commands must NOT do

- Open SQLite via `internal/db.OpenDB`
- Construct domain stores like `recally.NewStore(db)`
- Read or write files under `ANNA_HOME/library/...`
- Fetch external resources (RSS feeds, web pages) on behalf of the user — the
  server handles that and exposes a verb (`POST /api/recally/feeds/{id}/poll`)

A grep over `cmd/anna/` for `OpenDB`, `sqlc.`, or `NewFileManager` should turn
up only the server bootstrap (`gateway.go`).
