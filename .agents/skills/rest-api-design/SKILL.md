---
name: rest-api-design
description: >
  REST API design following Google's API Design Guide (AIP). Use when adding API endpoints,
  reviewing API specs, designing request/response schemas, discussing API naming, pagination,
  error handling, or response formats. Also use when the user mentions OpenAPI, resource-oriented
  design, or API conventions — even if they don't say "REST" explicitly.
---

# REST API Design

This skill follows Google's API Design Guide and its AIP (API Improvement Proposals). The core philosophy: APIs are **resource-oriented** — model your domain as resources (nouns) with a small set of standard methods (verbs), and let HTTP do what HTTP does.

Reference: <https://github.com/aip-dev/google.aip.dev> — the canonical AIP source. When this skill doesn't cover a topic, check the relevant AIP.

## Resource-Oriented Design

The fundamental building blocks are individually-named resources and the relationships between them. Design APIs around data models, not functions.

Three principles:

1. **Resources are nouns.** A resource is something you can name: a book, a user, a session. Resources have fields and may contain sub-resources.
2. **Methods are verbs.** A small number of standard methods (List, Get, Create, Update, Delete) cover most operations. Custom methods handle the rest.
3. **Stateless protocol.** Each request is independent. The server persists shared data; the client owns application state.

Do not mirror your database schema in the API surface. The API models what clients need, which often differs from internal storage.

### Resource hierarchy

Resources form a tree. A collection contains resources of the same type. Every resource belongs to exactly one parent collection (except top-level resources). The parent-child graph must be acyclic.

```
publishers/{publisherId}/books/{bookId}
agents/{agentId}/sessions/{sessionId}
```

Keep nesting shallow — two levels is typical, three is the practical maximum. Beyond that, promote the sub-resource to a top-level collection with a query filter.

## Standard Methods

Standard methods provide consistent semantics across all resources. Always use them instead of custom methods when they fit.

| Method | HTTP   | URI pattern        | Response         |
| ------ | ------ | ------------------ | ---------------- |
| List   | GET    | `/collection`      | Resource list    |
| Get    | GET    | `/collection/{id}` | Single resource  |
| Create | POST   | `/collection`      | Created resource |
| Update | PATCH  | `/collection/{id}` | Updated resource |
| Delete | DELETE | `/collection/{id}` | Empty            |

### List

Returns resources from a collection. Must support pagination (see Pagination section).

```
GET /v1/publishers/123/books?page_size=20&order_by=title

→ 200 OK
{
  "books": [ ... ],
  "next_page_token": "eyJ..."
}
```

- The response contains a repeated field named after the resource (`books`, `sessions`, `agents`)
- Include `next_page_token` when more results exist; set to `null` when no more pages
- May include `total_size` (can be an estimate; document if so)
- For soft-deleted resources, support `show_deleted=true` to include them

### Get

Returns a single resource. Every resource must support Get — it's how clients verify state after mutations.

```
GET /v1/publishers/123/books/456

→ 200 OK
{ "id": "456", "title": "Les Misérables", ... }
```

- Return the fully-populated resource
- Return 404 if the resource does not exist

### Create

Creates a resource in a collection.

```
POST /v1/publishers/123/books
{ "title": "Les Misérables", "author": "Victor Hugo" }

→ 201 Created
{ "id": "456", "title": "Les Misérables", ... }
```

- Return the created resource with all server-generated fields populated
- Accept client-specified IDs via a `{resource}_id` field in the body when the resource supports it
- Return 409 Conflict if a duplicate ID already exists (or 403 if the caller can't see the duplicate)

### Update

Modifies an existing resource. Always use PATCH for partial updates.

```
PATCH /v1/publishers/123/books/456
{ "title": "Updated Title" }

→ 200 OK
{ "id": "456", "title": "Updated Title", ... }
```

Why PATCH, not PUT: adding a new field to a resource won't break existing clients. With PUT, an old client that doesn't know about the new field would wipe it out on every update. PATCH is the only safe default for forward compatibility.

- Return the full updated resource
- Omitted fields remain unchanged
- Fields explicitly set to `null` are cleared

### Delete

Removes a resource.

```
DELETE /v1/publishers/123/books/456

→ 204 No Content
```

- Return 204 with empty body on success
- Return 404 if not found
- For parent resources with children, return 409 unless `force=true` is passed for cascading delete

### Custom methods

When standard methods don't fit, use POST with a colon-separated verb suffix:

```
POST /v1/publishers/123/books/456:archive
POST /v1/documents:translate
```

Naming: verb + noun, camelCase, no prepositions. `archiveBook`, not `getBookForArchiving`.

## Resource Names

Resources are identified by their name, which encodes the hierarchy:

```
publishers/123/books/les-miserables
agents/stella/sessions/abc-def
```

### Collection identifiers

- Plural, lowercase, hyphen-separated for multi-word: `books`, `publishers`, `agent-users`
- Use concise American English
- Avoid redundant parent prefixes: `users/123/events/456`, not `users/123/userEvents/456`

### Resource IDs

- Lowercase letters, numbers, hyphens: `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`
- Max 63 characters (DNS-safe)
- Aliases like `users/me` are fine but responses must always use the canonical ID

### In request/response messages

- The resource's own identifier field is `id`
- Parent references use `parent`: the parent's resource name
- Foreign key references: `{resource}_id` as a string field

## Field Names

- `snake_case` everywhere: request bodies, response bodies, query parameters
- American English, singular for scalar fields, plural for arrays
- No prepositions: `error_reason` not `reason_for_error`
- Booleans: use `is_` prefix for clarity (`is_admin`, `is_active`, `is_deleted`)
- Timestamps: ISO 8601 with timezone (`2025-05-26T10:30:00Z`), field name ends in `_at` (`created_at`, `updated_at`, `deleted_at`)
- URIs use `uri` field name; URLs use `url`
- Use well-known abbreviations: `config`, `id`, `spec`, `stats`, `info`

## Pagination

Every List method must support pagination. Use cursor-based pagination.

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

- Tokens are opaque — clients must not parse them. Base64-encode keyset values (e.g. `created_at` + `id`)
- Clients must not change request parameters between pages (except `page_size` and `page_token`); server returns 400 if they do
- Tokens expire after 72 hours
- The server may return fewer results than `page_size`, even mid-collection

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

Accept `order_by` as a comma-separated list of field names. Default is ascending; append `desc` for descending:

```
?order_by=created_at desc,title
```

## Errors

Use HTTP status codes and a structured error body.

### Error response structure

```json
{
  "error": {
    "code": 404,
    "message": "Book 'publishers/123/books/456' not found.",
    "status": "NOT_FOUND"
  }
}
```

Every error response uses this exact shape. The `code` mirrors the HTTP status, `status` is the canonical name, `message` is human-readable.

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

- Help a technical user understand and resolve the issue
- Be brief and actionable
- Never expose internal implementation details or stack traces

## Response Design

### Return the resource directly

HTTP provides the envelope — status codes, headers, content-type. Do not add another one inside the body.

**Do:**

```json
{ "id": "stella", "model": "claude-sonnet-4-20250514" }
```

**Don't:**

```json
{ "data": { "id": "stella", "model": "claude-sonnet-4-20250514" } }
```

A `{"data": ...}` wrapper adds no information that the HTTP status doesn't already carry. It forces every client to unwrap responses, creates a mismatch between the OpenAPI schema and the actual response, and is a source of bugs when someone forgets to unwrap.

### Return the full resource after mutations

After Create, Update, or custom methods that modify state, return the fully-populated resource. This lets clients update their local state without a follow-up Get.

### Consistent schema across methods

The resource schema must be the same across all methods. `GET /books/123` and the response from `PATCH /books/123` return the same shape.

### Output-only fields

Some fields are server-computed and not settable by clients (`created_at`, `updated_at`, computed aggregates). Document these as read-only. Ignore them silently if a client includes them in a Create or Update request.

## Naming Conventions

### URL paths

- Collection names: plural, lowercase, hyphen-separated for multi-word: `/publishers`, `/agent-users`
- No verbs in paths — HTTP methods are the verbs
- Path structure: `/api/{collection}/{id}/{sub-collection}/{sub-id}`
- API prefix (`/api/`) to distinguish from page routes

### Operation IDs (OpenAPI)

camelCase: `listBooks`, `getBook`, `createBook`, `updateBook`, `deleteBook`. For custom methods: `archiveBook`, `translateDocument`.

### Query parameters

`snake_case`: `page_size`, `page_token`, `order_by`, `show_deleted`

## Versioning

Use URL-based versioning: `/v1/books`, `/v2/books`. If the API is internal (served only to your own frontend), skip versioning and evolve API + client together.

Never break an existing field's type or meaning — add new fields instead. Remove fields only after confirming no client uses them.

## Design Process

1. **Identify the resources** — what are the nouns? Users, sessions, agents, jobs
2. **Define the hierarchy** — what belongs to what? Sessions belong to agents
3. **Map standard methods** — most resources need List + Get + Create + Update + Delete
4. **Add custom methods only when needed** — archive, export, run, cancel
5. **Design the schemas** — fields, types, required vs optional
6. **Write the spec first** — the spec is the source of truth, not the implementation
7. **Generate code** — use codegen to ensure spec and implementation stay in sync

## Review Checklist

Before approving any API change:

1. Is this modeled as resources with standard methods, or was a custom method forced where a standard one fits?
2. Does the response return the resource directly (no envelope)?
3. Is the resource schema consistent across all methods that return it?
4. Do list endpoints support pagination with `page_size` / `page_token` / `next_page_token`?
5. Are errors returned with `{ "error": { "code", "message", "status" } }` and the correct HTTP status?
6. Are field names `snake_case`?
7. Do Create and Update return the full resource?
8. Does Delete return 204 with no body?
9. Are collection names plural and resource IDs in the URL?
10. Is the custom method using `POST /resource:verb`?
