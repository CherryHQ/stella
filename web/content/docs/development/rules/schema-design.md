---
title: Database schema design rules
description: Schema design best practices for Stella, with the PostgreSQL mapping this project uses.
---

> This is a **rule file** for contributors. When you create a table, change a
> column, or plan a data model, read this page first. The general principles
> below are database-agnostic; the [PostgreSQL in this project](#postgresql-in-this-project)
> section pins them to Stella's actual stack (PostgreSQL + goose migrations). The
> migration workflow itself lives in `goose.md`.

## Start With the Domain

Model real business concepts: users, sessions, goals, tasks. Name tables after
entities. Avoid designing around screens or API responses — UI changes faster
than core data.

## Naming

One style, applied everywhere:

- Tables: `{group}_{entity}`, singular, `snake_case` — `auth_user`,
  `auth_session`, `agent_goal`, `agent_task`.
- Columns: `snake_case` — `user_id`, `created_at`, `status`.
- Indexes: `idx_{table}_{columns}` — `idx_auth_user_email`.
- Foreign keys: inline `REFERENCES` in the column definition.

**Group prefix**: Related tables share a short prefix so they sort together and
ownership is clear:

```
auth_user, auth_session, auth_identity
agent_goal, agent_task, agent_task_run
```

**Singular**: A table name is the entity type, not the collection. This keeps FK
references natural (`user_id` → `auth_user`), avoids irregular plurals, and
keeps compound names unambiguous (`order_item` not `order_items`).

Avoid vague names: `data`, `type`, `value`, `flag`, `status2`.

## Keys

- Every table gets a primary key. The default is a native `UUID` minted in Go
  with `uuid.NewV7()` (time-ordered, index-friendly), backed by a
  `DEFAULT uuidv7()` column default (see [PostgreSQL in this
  project](#postgresql-in-this-project)).
- Use a `TEXT` key only for a genuinely natural or non-uuid id: human slugs
  (`agent`, `channel`), external names (`provider`), short truncated ids
  (`sched_job`, `skill`), or legacy/deterministic ids that are not uuids
  (`ctx_conversation`, `agent_goal`, `ctx_summary`, recally ULIDs).
- Use natural keys only when genuinely immutable (ISO country codes, composite
  join-table PKs).
- Foreign keys on every relationship — the database must protect integrity.

## Required Columns

Every table must include an identifier and creation/update timestamps. They are
non-negotiable; no table ships without them. See the PostgreSQL section for the
exact column definitions Stella uses.

## Data Types

Choose the most specific type the database offers that fits the concept:

| Concept         | Intent                     | Why                                                       |
| --------------- | -------------------------- | --------------------------------------------------------- |
| Primary key     | Globally unique ID         | No coordination needed                                    |
| Timestamps      | Instant in time, UTC       | Real-world events need a stable, zone-aware instant       |
| Calendar dates  | Date with no time          | Birthdays, billing dates                                  |
| Money           | Integer cents (or decimal) | Never float — rounding errors compound                    |
| Boolean         | True/false                 | Not a free-form string                                    |
| Enum/state      | Bounded set of states      | Keep extensible value sets in code; see Constraints       |
| Structured blob | JSON                       | Only for metadata, external payloads, rarely-queried data |

Don't store everything as free text. The type system exists to catch bugs at the
boundary.

## Constraints

Use the database to enforce structural rules — application code can have bugs,
constraints cannot be bypassed.

Checklist:

- `NOT NULL` — default stance. Allow NULL only when absence is semantically
  meaningful.
- `UNIQUE` — enforce on natural identifiers.
- `CHECK` — validate ranges and invariants.
- `FOREIGN KEY ... ON DELETE` — choose CASCADE, SET NULL, or RESTRICT
  deliberately.
- Composite unique constraints for multi-column uniqueness.

**Default: keep enum value sets in Go, not in a `CHECK (col IN (...))`
constraint.** A value list in a `CHECK` can only change with a migration, and
most of these sets — statuses, kinds, scopes, review policies — grow as features
land. Stella enforces them in code (typed constants validated at the write
boundary) and keeps the column a plain `TEXT`.

**Exception: a genuinely closed, immutable enum may use a `CHECK`.** If the set
is fixed by an external contract and will never grow — `auth_policy.effect IN
('allow', 'deny')` is the canonical case — the `CHECK` is a cheap, permanent
safety net and belongs in the schema. The bar is "certain never to change," not
"small today." When in doubt, enforce in Go.

**Validate code-enforced enums at the write boundary.** Removing the `CHECK`
removes the database's backstop, so every untrusted entry point (HTTP/CLI
request bodies) must reject unknown values before persisting — return an
invalid-argument error, don't silently store. Internal call sites that already
pass typed constants are trusted and need no extra guard.

Keep the allowed set in one place: a small `valid<Enum>` helper (or a `Valid()`
method on a named type) colocated with the value constants. Validate by calling
it — never re-list the literals in scattered `if x != "a" && x != "b"` chains,
which drift the moment someone adds a value.

This is _not_ a ban on `CHECK` generally. Structural invariants always stay in
the database: range checks (`price_cents >= 0`), XOR/coupling rules (a run
belongs to a task _or_ a goal, never both), and self-reference guards
(`task_id != dep_task_id`). Those encode relationships between columns, not an
enumeration of one column's allowed values, and they don't churn when you add a
new status.

## Normalization

Start at third normal form:

- One fact in one place.
- No repeated columns (`phone_1`, `phone_2`, `phone_3`).
- No comma-separated IDs in a single field. Use a join table with a composite
  primary key instead.

Denormalize later only for measured performance reasons, and document when you
do.

## Relationships

| Pattern      | Implementation                                                             |
| ------------ | -------------------------------------------------------------------------- |
| One-to-many  | FK on the "many" side: `agent_goal.user_id REFERENCES auth_user(id)`       |
| Many-to-many | Join table with composite PK                                               |
| One-to-one   | FK as PK: `auth_user_profile.user_id PRIMARY KEY REFERENCES auth_user(id)` |

Make relationships explicit. Implicit relationships (matching IDs without FK
constraints) rot silently.

## Indexes

- Index every foreign key.
- Index columns in frequent WHERE, JOIN, ORDER BY.
- Composite indexes: equality columns first, range/sort columns last.
- Don't over-index — each index slows writes and consumes storage.
- Partial indexes for selective queries (`WHERE status = 'pending'`).

## Deletion Strategy

Decide per table — don't default to one approach everywhere:

| Strategy                   | When                                            |
| -------------------------- | ----------------------------------------------- |
| Hard delete                | Transient data, logs, ephemeral records         |
| Soft delete (`deleted_at`) | User-visible records needing undo or audit      |
| CASCADE                    | Tightly-coupled children (runs of a task)       |
| RESTRICT                   | Prevent silently orphaning important references |
| Archive                    | Move cold data to separate storage              |

CASCADE is convenient but dangerous — it can remove more than you expect. Never
combine CASCADE-on-history with self-deleting parents. A one-shot scheduler job
that deletes itself after firing will also delete run/audit rows that were just
written if those rows cascade from the parent. Retire fired or completed parents
as disabled rows instead; reserve deletion plus CASCADE for explicit user
deletion where dropping history is intended.

## JSON Columns

Use for:

- External payloads you don't control.
- Flexible per-record metadata.
- Rarely-queried settings or config.

Never use for:

- Core relational data you join, filter, or aggregate.
- Data that needs validation or uniqueness constraints.
- Anything that would benefit from a foreign key.

If you're querying inside JSON frequently, extract it into proper columns.

## Change Management

- Migrations only — never manual DDL in production.
- Prefer additive changes: add column → backfill → switch code → remove old
  column.
- New columns should be nullable or have defaults (avoid locking large tables).
- Never drop columns without verifying no code reads them.

## Security

- Hash passwords with bcrypt/argon2/scrypt — never reversible encryption.
- Never store secrets in plain text; Stella stores credential material encrypted
  via the vault.
- Separate sensitive data (PII, credentials) when access patterns differ.
- Retain personal data no longer than needed.

## Performance Basics

- Design around known access patterns — not hypothetical ones.
- Keep hot tables lean; archive cold data.
- Paginate large result sets (keyset pagination > offset).
- Avoid unbounded text search without full-text indexes.

## PostgreSQL in this project

Stella stores data in **PostgreSQL** (an embedded cluster by default), with
schema managed by **hand-written goose migrations**. Use native Postgres types
directly — the generic concepts above each have a real column type, no
SQLite-style encoding tricks. The schema source of truth is the goose migrations
in `internal/db/migrations/`; write each schema change as a new migration (see
`goose.md`).

### Type mapping

| Concept         | Column type (this project)           | Notes                                                      |
| --------------- | ------------------------------------ | ---------------------------------------------------------- |
| Primary key     | `UUID PRIMARY KEY DEFAULT uuidv7()`  | minted in Go with `uuid.NewV7()`; `TEXT` for natural ids   |
| Foreign key     | match the referenced PK type         | `UUID` to a uuid PK, `TEXT` to a text PK                   |
| Timestamps      | `TIMESTAMPTZ NOT NULL DEFAULT now()` | always zone-aware; store UTC                               |
| Boolean         | `BOOLEAN NOT NULL DEFAULT false`     | real `true`/`false`, not 0/1                               |
| Money           | `BIGINT` cents / `NUMERIC`           | integer cents for currency                                 |
| Enum/state      | `TEXT` (+ optional `CHECK`)          | valid values enforced in Go; `CHECK` only for closed enums |
| Structured blob | `JSONB NOT NULL DEFAULT '{}'`        | true JSON; query with `->`/`->>`/`jsonb_*`                 |
| Binary          | `BYTEA`                              | raw bytes                                                  |

`uuidv7()` is a native PostgreSQL 18 function. The column default covers inserts
that omit the id; application code still mints the id explicitly with
`uuid.NewV7()` so it is known before the row is written. A foreign-key column
must declare the **same** type as the primary key it references, so a `TEXT`
natural key (slug, ULID, legacy id) forces its referencing FKs to `TEXT` too.

### Required columns (canonical form)

```sql
CREATE TABLE project (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id     UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'draft',  -- valid values enforced in Go
    context     JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
```

### Timestamps are UTC

`now()` and `TIMESTAMPTZ` are zone-aware — never store a naive timestamp. Store
UTC, write `time.Now().UTC()` from Go, and serialize RFC3339 with a zone at the
API boundary. The full timezone contract is in the project `CLAUDE.md`.

## Review Checklist

Before approving any schema change:

1. Does every table follow `{group}_{entity}` singular naming?
2. Does every table have a primary key (`UUID` by default, `TEXT` only for a
   natural/legacy id), plus `created_at` and `updated_at`?
3. Does every table have a clear single purpose?
4. Are relationships enforced with foreign keys?
5. Are required fields marked `NOT NULL`?
6. Are uniqueness rules enforced?
7. Are timestamps `TIMESTAMPTZ` (UTC), booleans `BOOLEAN`, blobs `JSONB`, and
   money integer cents?
8. Are common queries supported by indexes?
9. Is deletion behavior intentional per table?
10. Did the change follow [`goose.md`](./goose.md) for the migration workflow,
    with no edits to already-committed migrations?
11. Is sensitive data protected?
