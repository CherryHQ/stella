# api/

## Layout

```
api/
  spec/                         ← OpenAPI source (human-edited)
    openapi.yaml                  root aggregator — paths + security
    components.yaml               ASSEMBLED — do not hand-edit
    components/
      common.yaml                 security schemes + shared responses
  domains/                      ← one sub-folder per domain (auto-discovered by codegen)
    recally/
      schemas.yaml                recally types (Article, Feed, Digest, …)
      paths.yaml                  recally REST paths
    scheduler/
      schemas.yaml                scheduler types (Job, JobList, …)
      paths.yaml                  scheduler REST paths
  codegen/                      ← oapi-codegen configs
    types.yaml                    → api/types/gen.go
    server.yaml                   → api/server/gen.go  (import-mapping → api/types)
    client.yaml                   → api/client/gen.go  (import-mapping → api/types)
  types/gen.go                  ← generated: all API types (package types)
  server/gen.go                 ← generated: ServerInterface + routing (package server)
  client/gen.go                 ← generated: HTTP client (package client)
```

## How it works

`api/spec/components.yaml` is auto-assembled by `mise run generate:api`:
```
spec/components/common.yaml + domains/*/schemas.yaml
    → (yq merge, glob — new domains picked up automatically)
    → spec/components.yaml   [DO NOT EDIT]
    → oapi-codegen           → types/gen.go / server/gen.go / client/gen.go
```

Domain paths files reference assembled schemas via `../../spec/components.yaml#/…`.
The server/client codegen maps both `./components.yaml` and `../../spec/components.yaml`
to `api/types` via import-mapping, so generated code imports types from `api/types`
instead of redeclaring them.

## Adding a schema

1. Edit `api/domains/<domain>/schemas.yaml`.
2. Run `mise run generate:api`.
3. Implement the handler — no other files to touch.

## Adding a domain

1. Create `api/domains/<domain>/schemas.yaml` with self-contained schemas.
2. Create `api/domains/<domain>/paths.yaml` with paths referencing `../../spec/components.yaml#/…`.
3. Add path `$ref`s to `api/spec/openapi.yaml`.
4. Run `mise run generate:api` — the yq glob picks up the new schemas automatically.
5. Implement the new `ServerInterface` methods in `internal/admin/`.
