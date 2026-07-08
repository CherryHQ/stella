# api/

API changes are spec-first: design schemas and paths in the OpenAPI domain files, regenerate code, then implement the generated server methods.

**Before adding or changing any endpoint, read `web/content/docs/development/rules/api-design.md`** — the binding API-design rule (resource modeling, standard/custom methods, pagination, structured errors, response shape). This file covers the _mechanics_ (spec layout + codegen); that rule covers the _design contract_.

## Layout

```
api/
  spec/                         ← OpenAPI source (human-edited)
    openapi.yaml                  root aggregator — paths + security
    components.yaml               ASSEMBLED — do not hand-edit
    components/
      common.yaml                 security schemes + shared responses
    domain/                       ← one sub-folder per domain (auto-discovered by codegen)
      agents/                       agent CRUD + skill management
      auth/                         login, logout, register, OAuth
      auth_users/                   admin user management
      channels/                     channel management
      plugins/                      plugin management + manifest plugins
      profile/                      user profile + vault
      recally/
        schemas.yaml                recally types (Article, Feed, Digest, …)
        paths.yaml                  recally REST paths
      scheduler/
        schemas.yaml                scheduler types (Job, JobList, …)
        paths.yaml                  scheduler REST paths
      sessions/                     chat session management
      …                             (agents, models, providers, skills, users, etc.)
  codegen/                      ← oapi-codegen configs
    types.yaml                    → api/types/gen.go
    server.yaml                   → api/server/gen.go  (import-mapping → api/types)
  types/gen.go                  ← generated: all API types (package types)
  server/gen.go                 ← generated: ServerInterface + routing (package server)
```

## How it works

`api/spec/components.yaml` is auto-assembled by `mise run generate:api`:

```
spec/components/common.yaml + spec/domain/*/schemas.yaml
    → (yq merge, glob — new domains picked up automatically)
    → spec/components.yaml   [DO NOT EDIT]
    → oapi-codegen           → types/gen.go / server/gen.go
```

Domain paths files reference assembled schemas via `../../components.yaml#/…`.
The server codegen maps both `./components.yaml` and `../../components.yaml`
to `api/types` via import-mapping, so generated code imports types from `api/types`
instead of redeclaring them.

## API change workflow

Every new or changed HTTP API must follow this workflow:

1. Add or update schemas in `api/spec/domain/<domain>/schemas.yaml`.
2. Add or update paths in `api/spec/domain/<domain>/paths.yaml`.
3. Add path `$ref`s in `api/spec/openapi.yaml`.
4. Run `mise run generate:api` to regenerate:
   - `api/spec/components.yaml` — assembled schema components; do not edit directly.
   - `api/types/gen.go` — shared API types (`package types`).
   - `api/server/gen.go` — server interface, routing helpers, and aliases to `types`.
5. Implement the generated server methods on `*Server` in `internal/server/`.
6. Verify `*Server` satisfies `apiserver.ServerInterface`.

## Adding a schema

1. Edit `api/spec/domain/<domain>/schemas.yaml`.
2. Run `mise run generate:api`.
3. Implement the generated handler method if the schema change affects server behavior.

## Adding a domain

1. Create `api/spec/domain/<domain>/schemas.yaml` with self-contained schemas.
2. Create `api/spec/domain/<domain>/paths.yaml` with paths referencing `../../components.yaml#/…`.
3. Add path `$ref`s to `api/spec/openapi.yaml`.
4. Run `mise run generate:api` — the yq glob picks up the new schemas automatically.
5. Implement the new `ServerInterface` methods in `internal/server/`.

## Rules

- Edit domain files in `api/spec/domain/<domain>/`; never edit `api/spec/components.yaml` directly.
- Domain path files reference schemas through `../../components.yaml`.
- Never hand-write server routing for any domain. All `/api/*` routing comes from `apiserver.HandlerFromMux(s, s.mux)` in `routes.go`.
- Enum constants live in `api/types`, not `api/server`. Import `api/types` directly when constants are needed.
