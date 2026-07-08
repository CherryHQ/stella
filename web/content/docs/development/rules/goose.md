# goose Migrations

Stella manages its PostgreSQL schema with [goose](https://github.com/pressly/goose).
Migrations are **hand-written** and are the **single source of truth** for the
schema — there is no declarative schema layer. (Atlas was removed: it cannot
manage pgvector's `vector` type, HNSW operator classes, or pg_search's `USING
bm25` access method, all of which the search stack needs.)

## Core concept

1. You write a migration with `-- +goose Up` / `-- +goose Down` sections.
2. `stellad` applies pending migrations automatically on startup
   (`internal/db/database.go` → `runMigrations`, via `goose.Provider`).
3. sqlc reads the same migrations to generate Go types — it applies only the
   `Up` sections to build its catalog.

The schema lives **only** in `internal/db/migrations/`. There is no
`schemas/tables/*.sql` mirror to keep in sync.

## Project layout

```
internal/db/
  migrations/
    20260620131914_postgres_baseline.sql   # full baseline schema
    20260707092307_add_provider_models_cache.sql
  queries/                                  # sqlc query files (separate concern)
```

## Migration file format

```sql
-- +goose Up
CREATE TABLE shop_order (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id     UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'pending',
    total_cents BIGINT NOT NULL CHECK (total_cents >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_shop_order_user_id ON shop_order (user_id);

-- +goose Down
DROP TABLE shop_order;
```

- Statements are split on `;`. For a statement that contains its own semicolons
  (a `CREATE FUNCTION` body, a `DO $$ ... $$` block), wrap it in
  `-- +goose StatementBegin` / `-- +goose StatementEnd`.
- Each migration runs in its own transaction (PostgreSQL DDL is transactional),
  so a failed migration rolls back cleanly.
- Always write a `Down` section. If a migration is genuinely irreversible (the
  baseline), make `Down` an explicit no-op (`SELECT 1;`) with a comment saying
  why, rather than leaving it empty.

## Runtime-managed objects (carve-outs)

Some objects are created outside the migration files and must NOT appear in them:

- **Extensions** (`pg_trgm`, `vector`, `pg_search`) — created by `ensureExtensions`
  in `internal/db/database.go` before any migration runs, because they require
  extension binaries (and `shared_preload_libraries` for pg_search) that a plain
  `CREATE EXTENSION` in a migration cannot guarantee. Never put `CREATE EXTENSION`
  in a migration.
- **`river_*` tables** — River owns and versions its job-queue schema itself via
  `rivermigrate` (`internal/db/river.go`). Keep them out of the app migrations.

## Workflow

### Add or change a table

1. Scaffold: `mise run db:migrate:new -- add_shop_order`
   (creates `internal/db/migrations/<timestamp>_add_shop_order.sql`).
2. Write the `Up` and `Down` SQL by hand.
3. Validate it parses: `mise run db:validate`.
4. Update or add sqlc queries in `internal/db/queries/`.
5. Regenerate Go code: `mise run generate`.
6. Build and test (`mise run build && mise run test`) — startup applies the
   migration to the test database, so a broken migration fails fast.

### Commands

| Command                             | What it does                                          |
| ----------------------------------- | ----------------------------------------------------- |
| `mise run db:migrate:new -- <name>` | Scaffold a new timestamped goose migration            |
| `mise run db:validate`              | Validate all migrations parse and are well-formed     |
| `mise run db:migrate:up`            | Apply pending migrations to `STELLA_DATABASE_URL`     |
| `mise run db:migrate:status`        | Show applied/pending status for `STELLA_DATABASE_URL` |

`stellad` applies migrations on startup, so `db:migrate:up` is only for manually
driving an external PostgreSQL.

## Rules

1. **Never edit a committed migration** — write a new one. Migrations are the
   historical record. The **one** exception is the schema baseline _while the
   PostgreSQL backend is pre-release and carries no production or shared dev
   data_: amending the baseline in place is cleaner than a create-then-drop
   forward migration, and a dev reset (below) is cheap. This exception ends the
   moment a release ships a database anyone else holds.
2. **Never delete migrations.**
3. **One logical change per migration** — don't batch unrelated changes.
4. **Forward-only in spirit** — to undo a shipped change, write a new migration
   that reverses it. `Down` sections exist for local iteration, not production
   rollback.
5. **No `CREATE EXTENSION`** in migrations (see carve-outs).
6. Migrations are the schema source of truth — if it's not in a migration, it
   isn't in the database.

## Resetting a dev database

The baseline has no real `Down`. To reset, drop and recreate the database (or
delete the embedded data directory under `~/.stella/postgres`) and let `stellad`
re-apply migrations from scratch. The PostgreSQL backend is new and carries no
production data, so a clean reset is cheap.

## Integration with sqlc

`sqlc.yaml` points `schema` at `internal/db/migrations`. sqlc parses the goose
files, applies only the `Up` sections, and generates Go types in `pkg/db/sqlc/`.
A migration that adds a column and the query that uses it ship together; run
`mise run generate` after editing either.
