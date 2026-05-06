---
name: atlas
description: >
  Atlas database migration tool best practices and usage. Use when creating migrations,
  modifying database schemas, troubleshooting migration errors, or answering questions
  about Atlas workflow (diff, apply, lint, hash, versioned migrations).
---

# Atlas Migrations

Atlas is a declarative database schema management tool. You declare the desired schema state, Atlas generates the migration SQL to get there.

## Core Concept

Atlas uses a **declarative** approach:

1. You edit schema source files (the desired state).
2. Atlas compares desired state vs. current migration history.
3. Atlas generates the migration SQL automatically.

You never write migration SQL by hand.

## Project Layout

```
atlas.hcl                          # Atlas configuration
internal/db/
  schemas/
    main.sql                       # Entry point — imports all tables
    tables/
      auth_user.sql                # One file per table
      auth_session.sql
      shop_order.sql
  migrations/
    20260317041843_description.sql # Generated migrations (never edit)
    atlas.sum                      # Integrity hash (never edit)
  queries/                         # sqlc query files (separate concern)
```

## Configuration (atlas.hcl)

```hcl
variable "dev_url" {
  type    = string
  default = "sqlite://dev?mode=memory"
}

env "local" {
  src = "file://internal/db/schemas/main.sql"
  dev = var.dev_url

  migration {
    dir = "file://internal/db/migrations"
  }
}
```

Key concepts:
- `src` — the desired schema (your source of truth)
- `dev` — a disposable database Atlas uses for diffing (in-memory is fine)
- `migration.dir` — where generated migrations live

## Schema Source Files

### Entry Point (`main.sql`)

Imports all table files in dependency order:

```sql
-- atlas:import tables/auth_user.sql
-- atlas:import tables/auth_session.sql
-- atlas:import tables/shop_order.sql
```

Order matters — tables with foreign keys must come after their referenced tables.

### Table Files

One file per table. Contains CREATE TABLE and CREATE INDEX statements:

```sql
CREATE TABLE shop_order (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','confirmed','shipped','completed')),
    total_cents INTEGER NOT NULL CHECK (total_cents >= 0),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_shop_order_user_id ON shop_order(user_id);
CREATE INDEX idx_shop_order_status ON shop_order(status, created_at DESC);
```

## Workflow

### Adding a New Table

1. Create `internal/db/schemas/tables/shop_order.sql` with the CREATE TABLE statement.
2. Add `-- atlas:import tables/shop_order.sql` to `main.sql` (after any tables it references).
3. Run: `mise run db:diff -- add_shop_order`
4. Review the generated migration in `internal/db/migrations/`.
5. Run code generation: `mise run generate`
6. Commit the schema file, migration file, and `atlas.sum` together.

### Modifying an Existing Table

1. Edit the table's `.sql` file in `internal/db/schemas/tables/`.
2. Run: `mise run db:diff -- describe_the_change`
3. Review the generated migration — Atlas handles the ALTER/recreate strategy.
4. Run: `mise run generate`
5. Commit schema + migration + `atlas.sum`.

### Removing a Table

1. Remove the `-- atlas:import` line from `main.sql`.
2. Delete the table's `.sql` file from `internal/db/schemas/tables/`.
3. Run: `mise run db:diff -- drop_table_name`
4. Review — ensure CASCADE doesn't remove unexpected data.
5. Commit.

## Commands

| Command | What it does |
|---------|-------------|
| `mise run db:diff -- <name>` | Generate migration from schema changes |
| `mise run db:hash` | Rehash migration directory (fix integrity after manual edits) |
| `mise run db:validate` | Validate migration directory integrity |
| `mise run db:migrate:apply` | Apply pending migrations to a database |
| `mise run db:migrate:lint` | Lint migrations for potential issues |
| `mise run db:schema:diff` | Compare live database with migration files |

## Migration File Rules

1. **Never hand-write migrations** — Atlas generates them.
2. **Never edit generated migrations** — regenerate instead.
3. **Never delete migrations** — they are the historical record.
4. **Always commit `atlas.sum`** — it's the integrity checksum.
5. **One logical change per migration** — don't batch unrelated changes.

## How Atlas Handles SQLite Limitations

SQLite has limited ALTER TABLE support. Atlas works around this by:

1. Creating a new table with the desired schema.
2. Copying data from the old table.
3. Dropping the old table.
4. Renaming the new table.

It wraps this in `PRAGMA foreign_keys = off` / `on` to avoid constraint violations during the swap. You don't need to worry about this — Atlas handles it automatically.

## Migration Naming

Use descriptive snake_case names that explain the change:

```
mise run db:diff -- add_shop_order
mise run db:diff -- add_user_email_column
mise run db:diff -- drop_legacy_tokens
mise run db:diff -- add_idx_order_status
```

The timestamp prefix is added automatically: `20260505150748_add_shop_order.sql`

## Best Practices

### Schema Design

- Keep schema files as the single source of truth — if it's not in the schema file, it shouldn't be in production.
- One table per file — makes diffs reviewable and conflicts manageable.
- Include indexes in the same file as the table they belong to.
- Order imports in `main.sql` by dependency (referenced tables first).

### Migration Safety

- Always review generated migrations before committing.
- Test migrations against a copy of production data for large tables.
- Prefer additive changes (add column with default) over destructive ones.
- When dropping columns: first remove all code that reads them, deploy, then drop.
- Never rename columns directly — add new, migrate data, drop old.

### Working With Others

- Generate migrations on a clean branch (no pending schema changes from others).
- If `atlas.sum` conflicts during merge: regenerate with `mise run db:hash`.
- If migrations conflict: resolve schema files first, then regenerate the migration.

### Rollbacks

Atlas migrations are forward-only. To "undo" a migration:

1. Edit the schema files to the desired state.
2. Generate a new forward migration that reverses the change.
3. Apply it.

Don't delete migrations from the history.

## Troubleshooting

| Problem | Fix |
|---------|-----|
| `atlas.sum` mismatch | Run `mise run db:hash` |
| "no changes detected" | Check that `main.sql` imports your new table file |
| Migration references wrong table | Check import order in `main.sql` |
| FK constraint error during migration | Atlas should handle this with PRAGMA — if not, ensure the referenced table exists |
| "checksum mismatch" in CI | Someone edited a committed migration — regenerate hash or fix the edit |

## Integration With sqlc

After generating a migration:

1. Write or update sqlc queries in `internal/db/queries/`.
2. Run code generation (`mise run generate`) — this runs both atlas and sqlc.
3. The generated Go code in `pkg/db/sqlc/` reflects the new schema.

Schema files are shared: Atlas reads them for migrations, sqlc reads them for type generation.

## Review Checklist

Before committing a migration:

1. Schema file edited (not the migration directly)?
2. `main.sql` import order respects foreign key dependencies?
3. Migration generated cleanly with `mise run db:diff`?
4. Generated SQL reviewed — no unexpected drops or data loss?
5. `atlas.sum` included in the commit?
6. sqlc queries updated if columns changed?
7. Code generation passes cleanly?
