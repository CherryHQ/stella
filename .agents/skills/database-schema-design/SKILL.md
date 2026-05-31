---
name: database-schema-design
description: >
  Database schema design best practices. Use when creating tables, reviewing migrations,
  planning data models, or answering schema design questions.
---

# Database Schema Design

## Start With the Domain

Model real business concepts: users, orders, invoices, events. Name tables after entities. Avoid designing around screens or API responses — UI changes faster than core data.

## Naming

One style, applied everywhere:

- Tables: `{group}_{entity}`, singular, `snake_case` — `auth_user`, `auth_session`, `sched_job`, `order_item`
- Columns: `snake_case` — `user_id`, `created_at`, `status`
- Indexes: `idx_{table}_{columns}` — `idx_auth_user_email`
- Foreign keys: inline `REFERENCES` in column definition

**Group prefix**: Related tables share a short prefix so they sort together and ownership is clear:

```
auth_user, auth_session, auth_identity
sched_job, sched_job_run
ctx_message, ctx_conversation, ctx_summary
```

**Singular**: A table name is the entity type, not the collection. This keeps FK references natural (`user_id` → `auth_user`), avoids irregular plurals (`identity` not `identities`), and keeps compound names unambiguous (`order_item` not `order_items`).

Avoid vague names: `data`, `type`, `value`, `flag`, `status2`.

## Keys

- Every table gets a UUID primary key: `id UUID PRIMARY KEY DEFAULT gen_random_uuid()`.
- Use `BIGINT` only when you have a specific reason (high-volume append-only tables, external system compatibility).
- Use natural keys only when genuinely immutable (ISO country codes, composite join-table PKs).
- Foreign keys on every relationship — the database must protect integrity.

## Required Columns

Every table must include:

```sql
id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
```

These are non-negotiable. No table ships without them.

## Data Types

Choose the most specific type that fits:

| Concept         | Type                           | Why                                                                            |
| --------------- | ------------------------------ | ------------------------------------------------------------------------------ |
| Primary key     | `UUID`                         | Globally unique, no coordination needed                                        |
| Timestamps      | `TIMESTAMPTZ`                  | Real-world events need timezone awareness                                      |
| Calendar dates  | `DATE`                         | Birthdays, billing dates — no time component                                   |
| Money           | `INTEGER` (cents) or `NUMERIC` | Never float — rounding errors compound                                         |
| Boolean         | `BOOLEAN`                      | Not integer, not text                                                          |
| Enum/state      | `TEXT`                         | Keep valid values in code so future extensions don't require schema migrations |
| Structured blob | `JSONB`                        | Only for metadata, external payloads, rarely-queried data                      |

Don't store everything as `TEXT`. The type system exists to catch bugs at the boundary.

## Constraints

Use the database to enforce structural rules — application code can have bugs, constraints cannot be bypassed. Do not add schema-level enums or enum-like `CHECK` constraints for values that may grow, such as `scope TEXT NOT NULL CHECK (scope IN ('system','agent','user'))`; enforce those allowed values in code so adding a new value does not require a schema migration.

```sql
CREATE TABLE shop_product (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sku TEXT NOT NULL UNIQUE,
    price_cents INTEGER NOT NULL CHECK (price_cents >= 0),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Checklist:

- `NOT NULL` — default stance. Allow NULL only when absence is semantically meaningful.
- `UNIQUE` — enforce on natural identifiers.
- `CHECK` — validate ranges and stable invariants, but not enums or extensible state lists.
- `FOREIGN KEY ... ON DELETE` — choose CASCADE, SET NULL, or RESTRICT deliberately.
- Composite unique constraints for multi-column uniqueness.

## Normalization

Start at third normal form:

- One fact in one place.
- No repeated columns (`phone_1`, `phone_2`, `phone_3`).
- No comma-separated IDs in a single field.

Bad:

```sql
user_ids TEXT -- "1,2,3"
```

Better:

```sql
CREATE TABLE org_team_member (
    team_id UUID NOT NULL REFERENCES org_team(id),
    user_id UUID NOT NULL REFERENCES auth_user(id),
    PRIMARY KEY (team_id, user_id)
);
```

Denormalize later only for measured performance reasons, and document when you do.

## Relationships

| Pattern      | Implementation                                                             |
| ------------ | -------------------------------------------------------------------------- |
| One-to-many  | FK on the "many" side: `shop_order.user_id REFERENCES auth_user(id)`       |
| Many-to-many | Join table with composite PK                                               |
| One-to-one   | FK as PK: `auth_user_profile.user_id PRIMARY KEY REFERENCES auth_user(id)` |

Make relationships explicit. Implicit relationships (matching IDs without FK constraints) rot silently.

## Indexes

- Index every foreign key.
- Index columns in frequent WHERE, JOIN, ORDER BY.
- Composite indexes: equality columns first, range/sort columns last.
- Don't over-index — each index slows writes and consumes storage.

```sql
CREATE INDEX idx_shop_order_user_id ON shop_order(user_id);
CREATE INDEX idx_shop_order_user_status_created
    ON shop_order(user_id, status, created_at DESC);
```

Partial indexes for selective queries:

```sql
CREATE INDEX idx_shop_order_pending ON shop_order(created_at)
    WHERE status = 'pending';
```

## Deletion Strategy

Decide per table — don't default to one approach everywhere:

| Strategy                   | When                                                  |
| -------------------------- | ----------------------------------------------------- |
| Hard delete                | Transient data, logs, ephemeral records               |
| Soft delete (`deleted_at`) | User-visible records needing undo or audit            |
| CASCADE                    | Tightly-coupled children (messages of a conversation) |
| RESTRICT                   | Prevent silently orphaning important references       |
| Archive                    | Move cold data to separate storage                    |

CASCADE is convenient but dangerous — it can remove more than you expect.

## JSON Columns

Use for:

- External payloads you don't control
- Flexible per-record metadata
- Rarely-queried settings or config

Never use for:

- Core relational data you join, filter, or aggregate
- Data that needs validation or uniqueness constraints
- Anything that would benefit from a foreign key

If you're querying inside JSON frequently, extract it into proper columns.

## Change Management

- Migrations only — never manual DDL in production.
- Prefer additive changes: add column → backfill → switch code → remove old column.
- New columns should be nullable or have defaults (avoid locking large tables).
- Never drop columns without verifying no code reads them.
- Test migrations against production-sized data — a 2-second migration on dev can lock for hours on prod.

## Security

- Hash passwords with bcrypt/argon2/scrypt — never reversible encryption.
- Never store secrets in plain text.
- Separate sensitive data (PII, credentials) when access patterns differ.
- Use column-level encryption for highly sensitive fields.
- Retain personal data no longer than needed.

## Performance Basics

- Design around known access patterns — not hypothetical ones.
- Keep hot tables lean; archive cold data.
- Paginate large result sets (keyset pagination > offset).
- Avoid unbounded text search without full-text indexes.
- Partition only when tables are genuinely large (millions+ rows).

## Review Checklist

Before approving any schema change:

1. Does every table follow `{group}_{entity}` singular naming?
2. Does every table have `id UUID`, `created_at`, and `updated_at`?
3. Does every table have a clear single purpose?
4. Are relationships enforced with foreign keys?
5. Are required fields marked `NOT NULL`?
6. Are uniqueness rules enforced?
7. Are money, dates, and timestamps using correct types?
8. Are common queries supported by indexes?
9. Is deletion behavior intentional per table?
10. Can this schema evolve safely with migrations?
11. Is sensitive data protected?
