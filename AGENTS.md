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
- **Before committing, run `mise run format` and `mise run test`.**
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

1. Add or update the schema in `api/spec/domain/<domain>/schemas.yaml` (domain source of truth).
2. Add endpoint paths in `api/spec/domain/<domain>/paths.yaml`.
3. Add the path `$ref` to `api/spec/openapi.yaml`.
4. Run `mise run generate:api` to regenerate:
   - `api/spec/components.yaml` — assembled from domain files (DO NOT EDIT)
   - `api/types/gen.go` — shared API types (`package types`)
   - `api/server/gen.go` — server interface + routing (`package server`, types are aliases to `types`)
   - `api/client/gen.go` — HTTP client (`package client`, types are aliases to `types`)
5. Implement the new method on `*Server` in `internal/admin/` (it must satisfy `apiserver.ServerInterface`).
6. Never hand-write server routing for recally/scheduler — it comes from `apiserver.HandlerFromMux(s, s.mux)` in `routes.go`.

See `api/CLAUDE.md` for the full spec layout and domain file conventions.

**Where generated code lives:**

| File | Package | Purpose |
|------|---------|---------|
| `api/types/gen.go` | `types` | All API data types (single copy, no duplication) |
| `api/server/gen.go` | `server` | `ServerInterface` + `HandlerFromMux` + type aliases to `types` |
| `api/client/gen.go` | `client` | HTTP client methods + type aliases to `types` |

**Codegen configs** live in `api/codegen/{types,server,client}.yaml`.

**Key rules:**
- Edit domain files in `api/spec/domain/<domain>/` — never edit `api/spec/components.yaml` directly.
- Domain path files reference `../../components.yaml` for all schemas.
- Enum constants (e.g. `FeedEntryStatusSaved`) live in `types`, not in `server`/`client`.
  Import `api/types` directly when you need them.

## Releases

Use `.agents/skills/release/SKILL.md` for the full release workflow.

Quick reference:
- `mise run release:check`
- `mise run release:snapshot`
- `mise run release`

Tag format: `vX.Y.Z`.
