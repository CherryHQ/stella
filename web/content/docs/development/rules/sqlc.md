---
title: sqlc queries
description: SQL query and code generation conventions for Stella's sqlc setup.
---

sqlc generates type-safe Go code from SQL queries. You write SQL, sqlc generates the Go.

## Configuration

```yaml
version: "2"
sql:
  - engine: "postgresql"
    queries: "internal/db/queries/*.sql"
    schema: "internal/db/migrations"
    gen:
      go:
        package: "sqlc"
        out: "pkg/db/sqlc"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_empty_slices: true
```

Key options:

- `emit_json_tags: true` — struct fields get `json:"snake_case"` tags
- `emit_empty_slices: true` — `:many` returns `[]T{}` instead of `nil` when no rows

## Query Annotations

Every query needs a name annotation comment:

```sql
-- name: <FunctionName> :<return_type>
```

Return types:

| Annotation  | Go signature       | Use when                                                  |
| ----------- | ------------------ | --------------------------------------------------------- |
| `:one`      | `(Model, error)`   | SELECT expecting exactly one row, INSERT/UPDATE RETURNING |
| `:many`     | `([]Model, error)` | SELECT returning multiple rows                            |
| `:exec`     | `error`            | INSERT/UPDATE/DELETE with no return value                 |
| `:execrows` | `(int64, error)`   | Only need rows affected count                             |

## Naming Conventions

Query function names should be `Verb` + `Entity` + optional `ByFilter` or `Qualifier`:

```sql
-- name: CreateOrder :one
-- name: GetOrder :one
-- name: GetOrderByUserID :one
-- name: ListOrder :many
-- name: ListOrderByStatus :many
-- name: UpdateOrder :exec
-- name: UpdateOrderStatus :exec
-- name: DeleteOrder :exec
-- name: CountOrder :one
```

Rules:

- **Entity is singular** — `GetOrder`, not `GetOrders`
- **List for collections** — `ListOrder`, not `GetOrders` or `FindOrders`
- **Verb prefixes**: `Create`, `Get`, `List`, `Update`, `Delete`, `Count`, `Upsert`
- **Qualifiers go last** — `ListOrderByStatus`, `GetUserByEmail`
- **Partial updates get a suffix** — `UpdateOrderStatus`, `UpdateUserRole`

## Writing Queries

### CRUD Basics

```sql
-- name: CreateOrder :one
INSERT INTO shop_order (id, user_id, status)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM shop_order WHERE id = $1;

-- name: ListOrder :many
SELECT * FROM shop_order ORDER BY created_at DESC;

-- name: UpdateOrder :exec
UPDATE shop_order
SET status = $1, updated_at = now()
WHERE id = $2;

-- name: DeleteOrder :exec
DELETE FROM shop_order WHERE id = $1;
```

### Use `RETURNING *` for Creates

Always use `RETURNING *` with `:one` for INSERT statements. This gives the caller the full populated model including defaults and generated values.

### Partial Updates

Write focused update queries instead of one giant "update everything" query:

```sql
-- name: UpdateOrderStatus :exec
UPDATE shop_order
SET status = $1, updated_at = now()
WHERE id = $2;

-- name: UpdateOrderShipping :exec
UPDATE shop_order
SET shipping_address = $1, shipping_method = $2, updated_at = now()
WHERE id = $3;
```

This is safer (no accidental overwrites), clearer (intent is obvious), and composes better than a single UpdateOrder that takes every column.

### Always Set `updated_at`

Every UPDATE query must set `updated_at = now()`.

### Filtering and Pagination

```sql
-- name: ListOrderByUser :many
SELECT * FROM shop_order
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: ListOrderByUserPaginated :many
SELECT * FROM shop_order
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;
```

For keyset pagination (better performance on large tables):

```sql
-- name: ListOrderAfterCursor :many
SELECT * FROM shop_order
WHERE user_id = $1 AND created_at < $2
ORDER BY created_at DESC
LIMIT $3;
```

### Slices (IN Clauses)

Use `sqlc.slice()` for dynamic IN clauses:

```sql
-- name: ListOrderByIDs :many
SELECT * FROM shop_order
WHERE id IN (sqlc.slice('ids'))
ORDER BY created_at DESC;
```

Go usage: `queries.ListOrderByIDs(ctx, []string{"id1", "id2", "id3"})`

### Named Parameters

Use `sqlc.arg()` when parameter names would be ambiguous or you want clearer Go function signatures:

```sql
-- name: ListVisibleSkill :many
SELECT * FROM skill
WHERE scope = 'system'
   OR (scope = 'agent' AND agent_id = sqlc.arg(agent_id))
   OR (scope = 'user'  AND user_id  = sqlc.arg(user_id))
ORDER BY created_at;
```

### Aggregates and Counts

```sql
-- name: CountOrderByStatus :one
SELECT COUNT(*) FROM shop_order WHERE status = $1;

-- name: GetOrderStats :one
SELECT
    COUNT(*) AS total,
    SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed,
    SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending
FROM shop_order
WHERE user_id = $1;
```

### Upserts

```sql
-- name: UpsertSetting :exec
INSERT INTO app_setting (key, value)
VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;
```

### Joins

```sql
-- name: GetOrderWithUser :one
SELECT
    o.*,
    u.username AS user_username
FROM shop_order o
JOIN auth_user u ON u.id = o.user_id
WHERE o.id = $1;
```

sqlc generates a custom struct for queries that don't map 1:1 to a single table.

### COALESCE for Nullable Aggregates

`MAX`/`SUM`/etc. return `NULL` over an empty set, which sqlc maps to a nullable
Go type. Wrap them in `COALESCE` so the column stays non-nullable:

```sql
-- name: GetMaxSeq :one
SELECT COALESCE(MAX(seq), 0)::bigint
FROM ctx_message WHERE conversation_id = $1;
```

The `::bigint` cast pins the result type when the expression would otherwise be
ambiguous (PostgreSQL's `::type` syntax; `CAST(... AS bigint)` works too).

## Transactions

sqlc generates a `WithTx` method. Use it for multi-query atomic operations:

```go
tx, err := pool.Begin(ctx)
if err != nil {
    return err
}
defer tx.Rollback(ctx)

qtx := queries.WithTx(tx)
order, err := qtx.CreateOrder(ctx, params)
if err != nil {
    return err
}
err = qtx.CreateOrderItem(ctx, itemParams)
if err != nil {
    return err
}
return tx.Commit(ctx)
```

## Nullable Columns

sqlc maps nullable columns to pgtype wrappers (`pgtype.Text`, `pgtype.Int8`, `pgtype.Timestamptz`, etc.). To avoid this:

- Prefer `NOT NULL DEFAULT ''` in schema to avoid nullable types entirely.
- When NULL is semantically meaningful, accept the `pgtype.*` types (check `.Valid` before reading).

## File Organization

One query file per table (or closely related group):

```
queries/
  auth_user.sql
  auth_session.sql
  auth_identity.sql
  shop_order.sql
  shop_order_item.sql
  ctx_message.sql
  ctx_conversation.sql
```

Keep queries in the same file as the table they primarily operate on. Cross-table joins go in the file of the "owning" entity.

## Common Mistakes

1. **Missing `updated_at`** — Every UPDATE must set it.
2. **Using `:exec` when you need the result** — Use `:one` with `RETURNING *` for inserts.
3. **Giant update queries** — Split into focused partial updates.
4. **Forgetting ORDER BY on `:many`** — Unordered results are unpredictable.
5. **`SELECT *` on joins** — Explicitly select columns or use table aliases to avoid ambiguity.
6. **Not using `COALESCE`** — aggregate functions like `MAX`/`SUM` return NULL over an empty set, forcing a nullable Go type even for integer columns.
7. **Mutable default in schema** — `DEFAULT now()` is evaluated at insert time (correct), not at schema creation.

## Workflow

1. Write or edit `.sql` query files.
2. Run sqlc generate (e.g., `mise run generate`).
3. Check generated-code drift with `mise run generate:check`.
4. Use the generated `*Queries` methods in application code.
5. Never edit generated files — they are overwritten on each generate.

## Review Checklist

Before committing query changes:

1. Every query has a clear, correctly-typed annotation?
2. Naming follows `Verb` + `Entity` + `Qualifier` pattern?
3. Every INSERT uses `RETURNING *` with `:one`?
4. Every UPDATE sets `updated_at`?
5. `:many` queries have ORDER BY?
6. Nullable columns are intentional, not accidental?
7. Generated code compiles cleanly?
8. No hand-edits in the generated output directory?
