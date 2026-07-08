---
title: API design rules
description: REST API conventions for Stella, following Google's API Improvement Proposals (AIP).
---

> This is a **rule file** for contributors. When you add or change any HTTP API,
> read this page first and follow it. The spec-first codegen workflow lives in
> [`api/CLAUDE.md`](https://github.com/CherryHQ/stella/blob/main/api/CLAUDE.md);
> this page is the design contract those specs must satisfy.

Stella follows Google's API Design Guide and its AIP (API Improvement Proposals).
The core philosophy: APIs are **resource-oriented** — model the domain as
resources (nouns) with a small set of standard methods (verbs), and let HTTP do
what HTTP does.

Reference: <https://github.com/aip-dev/google.aip.dev> — the canonical AIP source.
When this page doesn't cover a topic, check the relevant AIP.

## Resource-Oriented Design

The fundamental building blocks are individually-named resources and the
relationships between them. Design APIs around data models, not functions.

Three principles:

1. **Resources are nouns.** A resource is something you can name: a session, a
   user, an agent. Resources have fields and may contain sub-resources.
2. **Methods are verbs.** A small number of standard methods (List, Get, Create,
   Update, Delete) cover most operations. Custom methods handle the rest.
3. **Stateless protocol.** Each request is independent. The server persists
   shared data; the client owns application state.

Do not mirror the database schema in the API surface. The API models what
clients need, which often differs from internal storage.

### Resource hierarchy

Resources form a tree. A collection contains resources of the same type. Every
resource belongs to exactly one parent collection (except top-level resources).
The parent-child graph must be acyclic.

```
agents/{agentId}/sessions/{sessionId}
plugins/{kind}/{name}/config
```

Keep nesting shallow — two levels is typical, three is the practical maximum.
Beyond that, promote the sub-resource to a top-level collection with a query
filter.

## Standard Methods

Standard methods provide consistent semantics across all resources. Always use
them instead of custom methods when they fit.

| Method | HTTP   | URI pattern        | Response         |
| ------ | ------ | ------------------ | ---------------- |
| List   | GET    | `/collection`      | Resource list    |
| Get    | GET    | `/collection/{id}` | Single resource  |
| Create | POST   | `/collection`      | Created resource |
| Update | PATCH  | `/collection/{id}` | Updated resource |
| Delete | DELETE | `/collection/{id}` | Empty            |

### List

Returns resources from a collection. Must support pagination (see Pagination).

```
GET /api/agents/stella/sessions?page_size=20&order_by=created_at desc

→ 200 OK
{
  "sessions": [ ... ],
  "next_page_token": "eyJ..."
}
```

- The response contains a repeated field named after the resource (`sessions`,
  `tasks`, `agents`) — **never a bare JSON array**.
- Include `next_page_token` when more results exist; set to `null` when no more
  pages.
- May include `total_size` (can be an estimate; document if so).
- For soft-deleted resources, support `show_deleted=true` to include them.

### Get

Returns a single resource. Every resource must support Get — it's how clients
verify state after mutations.

```
GET /api/agents/stella/sessions/abc-def

→ 200 OK
{ "id": "abc-def", "title": "…", ... }
```

- Return the fully-populated resource.
- Return 404 if the resource does not exist.

### Create

Creates a resource in a collection.

```
POST /api/agents/stella/sessions
{ "title": "…" }

→ 201 Created
{ "id": "abc-def", "title": "…", ... }
```

- Return the created resource with all server-generated fields populated.
- Accept client-specified IDs via a `{resource}_id` field in the body when the
  resource supports it.
- Return 409 Conflict if a duplicate ID already exists (or 403 if the caller
  can't see the duplicate).

### Update

Modifies an existing resource. Always use PATCH for partial updates.

```
PATCH /api/auth/users/123
{ "role": "admin" }

→ 200 OK
{ "id": "123", "role": "admin", "is_active": true, ... }
```

Why PATCH, not PUT: adding a new field to a resource won't break existing
clients. With PUT, an old client that doesn't know about the new field would
wipe it out on every update. PATCH is the only safe default for forward
compatibility.

- Return the **full updated resource with HTTP 200** — not 204. (The auth-user
  role/active/agents updates were converted from 204 to 200 + resource body for
  exactly this reason.)
- Omitted fields remain unchanged.
- Fields explicitly set to `null` are cleared.

### Delete

Removes a resource.

```
DELETE /api/agents/stella/sessions/abc-def

→ 204 No Content
```

- Return 204 with empty body on success.
- Return 404 if not found.
- For parent resources with children, return 409 unless `force=true` is passed
  for cascading delete.

### Custom methods

When standard methods don't fit, append the action as a **trailing path
segment**:

```
POST /api/agents/stella/tasks/{id}/cancel
POST /api/goals/{id}/activate
POST /api/manifest-plugins/sync
```

Naming: verb + noun, camelCase operationId, no prepositions. `cancelTask`, not
`getTaskForCancelling`.

**Why a path segment, not AIP's colon (`:cancel`).** This project routes with
the Go 1.22 `http.ServeMux`. A wildcard like `{id}` must occupy a whole path
segment, so `/tasks/{id}:cancel` puts a colon inside the wildcard segment and
the mux **panics at registration** (`bad wildcard segment must end with '}'`).
The colon only works on a fully-literal segment (`/manifest-plugins:sync` would
route, but `/tasks/{id}:cancel` cannot). Rather than split the convention —
colon for collection-level actions, slash for per-id actions — we use **one rule
everywhere**: the action is a trailing path segment, no colons anywhere. AIP
itself accepts `/{id}/action` as the sub-resource fallback for custom methods.

So: **`POST /collection[/{id}]/action`** for every custom method. This is a hard
constraint of the stdlib mux, not a style preference. If the project ever
migrates off `ServeMux` to a router that handles `{id}:action` (e.g. chi),
revisit and prefer AIP's colon form.

## Resource Names

Resources are identified by their name, which encodes the hierarchy:

```
agents/stella/sessions/abc-def
plugins/channel/telegram/config
```

### Collection identifiers

- Plural, lowercase, hyphen-separated for multi-word: `sessions`, `agents`,
  `manifest-plugins`.
- Use concise American English.
- Avoid redundant parent prefixes: `users/123/events/456`, not
  `users/123/userEvents/456`.

### Resource IDs

- Lowercase letters, numbers, hyphens: `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`.
- Max 63 characters (DNS-safe).
- Aliases like `users/me` are fine but responses must always use the canonical
  ID.

### In request/response messages

- The resource's own identifier field is `id`.
- Parent references use `parent`: the parent's resource name.
- Foreign key references: `{resource}_id` as a string field.

## Field Names

- `snake_case` everywhere: request bodies, response bodies, query parameters.
- American English, singular for scalar fields, plural for arrays.
- No prepositions: `error_reason` not `reason_for_error`.
- Booleans: use `is_` prefix for clarity (`is_admin`, `is_active`, `is_deleted`).
- Timestamps: ISO 8601 / RFC3339 with timezone (`2025-05-26T10:30:00Z`), field
  name ends in `_at` (`created_at`, `updated_at`, `deleted_at`). Never emit the
  naive `"2006-01-02 15:04:05"` form — see the timezone rules in the project
  `CLAUDE.md`.
- URIs use `uri` field name; URLs use `url`.
- Use well-known abbreviations: `config`, `id`, `spec`, `stats`, `info`.

## Pagination

Every paginated List method uses cursor-based pagination with these names.

### Request fields

| Field        | Type   | Description                                                                             |
| ------------ | ------ | --------------------------------------------------------------------------------------- |
| `page_size`  | int    | Max results per page. Optional; server default is 20, max is 500. Negative values → 400 |
| `page_token` | string | Opaque token from a previous response's `next_page_token`. Omit on first request        |

### Response fields

| Field             | Type   | Description                                                    |
| ----------------- | ------ | -------------------------------------------------------------- |
| `{resources}`     | array  | The result set for this page. Named after the resource type    |
| `next_page_token` | string | Token for the next page. `null` when there are no more results |
| `total_size`      | int    | Optional. Total item count; may be an estimate                 |

### Rules

- Tokens are opaque — clients must not parse them. Stella encodes a base64
  offset; treat it as a cursor, not a number.
- Clients must not change request parameters between pages (except `page_size`
  and `page_token`); the server returns 400 if they do.
- The server may return fewer results than `page_size`, even mid-collection.
- **`page_size`/`page_token`/`next_page_token` applies to paginated collections**
  (sessions, tasks, stored digests). Result-cap and action params that are _not_
  paginated collections (e.g. article list caps, feed polling, session message
  fetches, skills search) keep their existing parameter names — don't rename a
  result cap to `page_size`.

## Filtering and Ordering

### Filtering

List methods accept field-level filters as query parameters:

```
?status=active&kind=chat
```

For free-text search, use `q`:

```
?q=search+term
```

### Ordering

Accept `order_by` as a comma-separated list of field names. Default is
ascending; append `desc` for descending:

```
?order_by=created_at desc,title
```

## Errors

Every error response uses HTTP status codes **and** this structured body
(AIP-193). The canonical `Error` schema lives in
`api/spec/components/common.yaml`; the shared error responses reference it.

### Error response structure

```json
{
  "error": {
    "code": 404,
    "message": "Session 'agents/stella/sessions/abc' not found.",
    "status": "NOT_FOUND"
  }
}
```

Every error uses this shape — **never the flat `{"error": "message"}`
form**. The `code` mirrors the HTTP status, `status` is the canonical name,
`message` is human-readable. Some errors may include an optional `details`
object for machine-readable context; detail keys use `snake_case` and must be
safe to expose to clients.

### Status code reference

| Code | Status                | When to use                                                                                 |
| ---- | --------------------- | ------------------------------------------------------------------------------------------- |
| 200  | OK                    | Successful Get, List, Update                                                                |
| 201  | Created               | Successful Create                                                                           |
| 204  | No Content            | Successful Delete                                                                           |
| 400  | Bad Request           | Malformed input, validation failure, invalid `page_token`                                   |
| 401  | Unauthorized          | Missing or invalid authentication                                                           |
| 403  | Forbidden             | Authenticated but insufficient permissions. Check permissions **before** resource existence |
| 404  | Not Found             | Resource doesn't exist. Also use for cross-tenant access to avoid leaking existence         |
| 409  | Conflict              | Duplicate resource, state conflict, etag mismatch                                           |
| 429  | Too Many Requests     | Rate limit exceeded                                                                         |
| 500  | Internal Server Error | Unhandled server failure                                                                    |

### Error message guidelines

- Help a technical user understand and resolve the issue.
- Be brief and actionable.
- Never expose internal implementation details or stack traces.
- When a behavior change turns an error path into a routine user action, export
  the sentinel error and map it to a 4xx. Unexported sentinels fall through as
  HTTP 500.

## Response Design

### Return the resource directly

HTTP provides the envelope — status codes, headers, content-type. Do not add
another one inside the body.

**Do:**

```json
{ "id": "stella", "model": "claude-sonnet-4-20250514" }
```

**Don't:**

```json
{ "data": { "id": "stella", "model": "claude-sonnet-4-20250514" } }
```

A `{"data": ...}` wrapper adds no information that the HTTP status doesn't
already carry. It forces every client to unwrap responses, creates a mismatch
between the OpenAPI schema and the actual response, and is a source of bugs.

The one exception is List, which names the array after the resource and adds
`next_page_token` — that is the pagination envelope, not a generic wrapper.

### Return the full resource after mutations

After Create, Update, or custom methods that modify state, return the
fully-populated resource. This lets clients update their local state without a
follow-up Get.

### Consistent schema across methods

The resource schema must be the same across all methods. `GET /sessions/123`
and the response from `PATCH /sessions/123` return the same shape.

### Output-only fields

Some fields are server-computed and not settable by clients (`created_at`,
`updated_at`, computed aggregates). Document these as read-only. Ignore them
silently if a client includes them in a Create or Update request.

## Plugin resource tree

Plugin sub-resources are nested under the plugin resource, not flattened into
sibling top-level paths:

```
GET /api/plugins/{kind}/{name}              # the plugin
GET /api/plugins/{kind}/{name}/status
GET /api/plugins/{kind}/{name}/config
GET /api/plugins/{kind}/{name}/config-schema
```

The old flat forms (`/api/plugin-status/{kind}/{name}`,
`/api/plugin-config/{kind}/{name}`, `/api/plugin-config-schema/{kind}/{name}`)
are gone. Model plugin facets as sub-resources of the plugin.

## Versioning

Stella's API is internal — served only to the CLI, Web UI, and first-party
clients — so it is unversioned and evolves together with its clients. Never
break an existing field's type or meaning; add new fields instead, and remove
fields only after confirming no client uses them.

## Design Process

1. **Identify the resources** — what are the nouns? Sessions, agents, tasks,
   goals.
2. **Define the hierarchy** — what belongs to what? Sessions belong to agents.
3. **Map standard methods** — most resources need List + Get + Create + Update +
   Delete.
4. **Add custom methods only when needed** — cancel, activate, sync.
5. **Design the schemas** — fields, types, required vs optional.
6. **Write the spec first** — the OpenAPI spec is the source of truth. Follow
   the workflow in `api/CLAUDE.md`.
7. **Generate code** — `mise run generate:api` keeps spec and implementation in
   sync.

## Review Checklist

Before approving any API change:

1. Is this modeled as resources with standard methods, or was a custom method
   forced where a standard one fits?
2. Does the response return the resource directly (no generic envelope)?
3. Is the resource schema consistent across all methods that return it?
4. Do list endpoints return a resource-named array (not a bare array) and
   support `page_size` / `page_token` / `next_page_token` when paginated?
5. Are errors returned with `{ "error": { "code", "message", "status" } }`,
   optional safe `details` only when clients need machine-readable context, and
   the correct HTTP status?
6. Are field names `snake_case`?
7. Do Create and Update return the full resource (Update with 200, not 204)?
8. Does Delete return 204 with no body?
9. Are collection names plural and resource IDs in the URL?
10. Is the custom method using `POST /collection[/{id}]/action` (trailing path
    segment, no colon)?
