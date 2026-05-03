# anna

## Core rules

- Prefer small, single-purpose files and shared helpers over duplicated logic.
- Make small, complete, reviewable changes.
- Do not guess. If required intent is ambiguous, ask.
- Always propagate errors and remove dead code or stale comments.

## Commands

- Use `mise tasks` to discover available tasks.
- Always run project workflows through `mise run <task>` instead of calling tools directly.

## Plugins

- Before changing plugins, read `docs/content/docs/plugins/`.
- Treat plugin docs as the source of truth.
- Verify plugin changes with `mise run test`.

## Database migrations

Schema source of truth: `internal/db/schemas/tables/*.sql`.

Workflow:
1. Edit schema files.
2. Run `mise run db:diff -- <name>`.
3. Run `mise run generate`.
4. Commit generated migrations with `internal/db/migrations/atlas.sum`.

- Never hand-write migration SQL.

## Testing and quality

- Do not run Go tests with `-race` locally by default.
- Use `mise run test` and `mise run test:coverage` locally.
- Reserve race-enabled runs for CI.
- Before committing, run `mise run format`.
- Use emoji-prefixed conventional commits such as `✨ feat:` and `🐛 fix:`.

## Documentation

When behavior, APIs, config, commands, or architecture change:

- Update `README.md` and/or `docs/content/docs/` as needed.
- Keep `README.md` concise; put detailed content in `docs/content/docs/`.
- Add new docs to the appropriate folder `meta.json` when needed.
- Keep `internal/agent/runner/builtin/anna/` in sync with user-facing changes.
- Docs are maintained in English (`*.md`, `*.mdx`) and Chinese (`*.zh.md`, `*.zh.mdx`) only. Always update both when adding or changing doc content. No other locales.

## API-first design

Every new HTTP API must follow this workflow:

1. Define the endpoint in the appropriate OpenAPI spec under `api/`:
   - `api/recally.yaml` — recally articles/feeds/digest endpoints
   - `api/scheduler.yaml` — scheduler job endpoints
   - `api/openapi.yaml` — aggregated spec (imports all sub-specs; add inline schemas here)
2. Run `mise run generate:api` to regenerate:
   - `api/server/gen.go` — server interface + routing (`package server`)
   - `api/client/gen.go` — HTTP client + types (`package client`)
3. Implement the new method on `*Server` in `internal/admin/` (it must satisfy `server.ServerInterface`).
4. Never hand-write server routing for recally/scheduler — it comes from `apiserver.HandlerFromMux(s, s.mux)` in `routes.go`.

**Where generated code lives:**

| File | Package | Purpose |
|------|---------|---------|
| `api/server/gen.go` | `server` | `ServerInterface` + `HandlerFromMux` + all param/response types |
| `api/client/gen.go` | `client` | HTTP client methods + all shared types |

**Key rule:** `api/openapi.yaml` is the single source of truth for codegen. It must have all schemas inlined in `components/schemas` (path-level `$ref` to sub-specs is fine; schema-level `$ref` to sub-specs is not — oapi-codegen won't follow them for type generation).

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

Quick reference:
- `mise run release:check`
- `mise run release:snapshot`
- `mise run release`

Tag format: `vX.Y.Z`.
