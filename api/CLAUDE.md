# api/

## Layout

```
api/
  spec/                         ← OpenAPI source (human-edited)
    openapi.yaml                  root aggregator — paths + security
    components.yaml               ASSEMBLED — do not hand-edit
    components/
      common.yaml                 security schemes + shared responses
  codegen/                      ← oapi-codegen configs
    types.yaml                    → api/types/types.gen.go
    server.yaml                   → api/server/gen.go  (import-mapping → apitypes)
    client.yaml                   → api/client/gen.go  (import-mapping → apitypes)
  types/types.gen.go            ← generated: all API types (package apitypes)
  server/gen.go                 ← generated: ServerInterface + routing (package apiserver)
  client/gen.go                 ← generated: HTTP client (package apiclient)
```

## How it works

`api/spec/components.yaml` is auto-assembled by `mise run generate:api` from the three domain files:
```
components/common.yaml + components/recally.yaml + components/scheduler.yaml
    → (yq merge)
    → components.yaml   [DO NOT EDIT]
    → oapi-codegen      → types.gen.go / server.gen.go / client.gen.go
```

Path files (`recally.yaml`, `scheduler.yaml`) reference schemas via `./components.yaml#/…`.
The server/client codegen maps `./components.yaml` to `apitypes` via import-mapping,
so generated server/client code imports types from `api/types` instead of redeclaring them.

## Adding a schema

1. Edit the appropriate `api/spec/components/<domain>.yaml`.
2. Run `mise run generate:api`.
3. Implement the handler — no other files to touch.

## Adding a domain

1. Create `api/spec/components/<domain>.yaml` with self-contained schemas.
2. Create `api/spec/<domain>.yaml` with paths referencing `./components.yaml#/…`.
3. Add path `$ref`s to `api/spec/openapi.yaml`.
4. Run `mise run generate:api` — the assembly picks up the new component file automatically.
5. Implement the new `ServerInterface` methods in `internal/admin/`.
