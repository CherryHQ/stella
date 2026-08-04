---
title: goose migrations
description: PostgreSQL migration rules for Stella's hand-written goose migrations.
---

Stella manages its PostgreSQL schema with [goose](https://github.com/pressly/goose).
Migrations are **hand-written** and are the **single source of truth** for the
schema — there is no declarative schema layer. Atlas was removed because it
cannot manage the pgvector and pg_search objects the search stack needs.

## Core concept

1. Write a migration with `-- +goose Up` / `-- +goose Down` sections.
2. `stellad` applies pending migrations automatically on startup
   (`internal/db/database.go` → `runMigrations`, via `goose.Provider`).
3. sqlc reads the same migrations and applies their `Up` sections to build its
   catalog.

The schema lives only in `internal/db/migrations/`. There is no declarative
schema mirror to keep in sync.

## Version transition and concurrency

Historical migrations use immutable timestamp versions through
`20260804120000`. `90000000000000_sequential_versioning.sql` is a documented
no-op anchor: it is deliberately 14 digits and lexically after the historical
`2`-prefixed files, so Goose and sqlc use the same order.

Every future migration must use the next contiguous integer:

```text
20260804120000  # final timestamp migration
90000000000000  # no-op sequential anchor
90000000000001  # first future migration
90000000000002  # next future migration
```

Create one only with:

```sh
mise run db:migrate:new -- add_shop_order
```

The task passes Goose's `-s` flag, which continues from the anchor. Do **not**
run `goose fix`: it would renumber the immutable historical files.

Concurrent branches can select the same next number. The repository test
rejects duplicate or skipped sequential versions in every checkout, including a
merge-queue checkout. Rebase or update from `main`, then rename only your
unmerged migration to the next version reported by the test. Never rename,
edit, or delete a migration already merged into `main`.

## Migration file format

```sql
-- +goose Up
CREATE TABLE shop_order (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE shop_order;
```

- Statements are split on `;`. Wrap a statement containing its own semicolons
  (such as `CREATE FUNCTION` or `DO $$ ... $$`) in
  `-- +goose StatementBegin` / `-- +goose StatementEnd`.
- Each migration runs in its own transaction, so PostgreSQL rolls back a failed
  migration cleanly.
- Always write a `Down` section. For a genuinely irreversible migration, make
  it an explicit `SELECT 1;` no-op with a comment explaining why.

## Runtime-managed objects

Do not add these to application migrations:

- **Extensions** (`pg_trgm`, `vector`, `pg_search`) are installed by
  `ensureExtensions` before migrations because they require installed binaries
  and, for pg_search, `shared_preload_libraries`.
- **`river_*` tables** are owned and versioned by River via `rivermigrate`.

## Workflow

1. Create the next version with `mise run db:migrate:new -- <name>`.
2. Write the `Up` and `Down` SQL.
3. Validate parsing with `mise run db:validate`.
4. Update related sqlc queries and run `mise run generate`.
5. Run `mise run build && mise run test`; startup applies migrations to the
   test database.

| Command                             | What it does                                           |
| ----------------------------------- | ------------------------------------------------------ |
| `mise run db:migrate:new -- <name>` | Scaffold the next sequential SQL migration             |
| `mise run db:validate`              | Validate all migration files parse and are well-formed |
| `mise run db:migrate:up`            | Apply pending migrations to `STELLA_DATABASE_URL`      |
| `mise run db:migrate:status`        | Show applied/pending status for `STELLA_DATABASE_URL`  |

`stellad` applies migrations on startup, so `db:migrate:up` is only for
manually driving an external PostgreSQL instance.

## Rules and repair

1. Migrations merged into `main` are immutable: never edit, rename, or delete
   them. Write a new migration instead.
2. Keep one logical change per migration.
3. Changes are forward-only in spirit. `Down` supports local iteration, not a
   production rollback plan.
4. Never use `CREATE EXTENSION` in a migration.
5. Do not enable Goose's `AllowOutOfOrder` at startup. `goose -allow-missing`
   is only a backup-first repair for an already-diverged development or
   operations database, never a normal startup setting.

## Resetting a development database

The baseline has no real `Down`. To reset, drop and recreate the database (or
delete `~/.stella/postgres` for embedded PostgreSQL), then let `stellad` apply
migrations from scratch.

## Integration with sqlc

`sqlc.yaml` points `schema` at `internal/db/migrations`. sqlc applies the `Up`
sections and generates Go types in `pkg/db/sqlc/`. A migration and queries that
depend on it ship together; run `mise run generate` after changing either.
