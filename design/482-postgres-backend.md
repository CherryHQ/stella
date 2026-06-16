# Design: Optional PostgreSQL backend (#482)

> Status: **Proposal — for review**. No code changes. Decision requested before implementation.
> Parent: #477.

## What

Evaluate adding PostgreSQL as an optional database backend while keeping SQLite as the
default. This document lays out the real cost, the architectural fork, and a recommendation,
so #477/#482 can decide direction before anyone writes code.

## Why (restated from the issue)

The issue wants: multiple replicas, higher concurrent read/write, managed backups/ops, better
k8s/cloud fit. **Note these are deployment/ops needs — none of them require the PostgreSQL SQL
_dialect_ specifically.** That distinction drives the recommendation below.

## Current state (what we'd be changing)

| Area             | Today                                                                                                                                                                                  | Files                                                                                     |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- |
| sqlc             | Single `engine: sqlite`, 371 queries → 47 generated files, whole app imports `pkg/db/sqlc` directly                                                                                    | `sqlc.yaml`, `internal/db/queries/*.sql` (67), `pkg/db/sqlc/*.sql.go` (47)                |
| Migrations       | Atlas declarative, dev DB `sqlite://?mode=memory`, 24 migrations                                                                                                                       | `atlas.hcl`, `internal/db/schemas/tables/*.sql` (59), `internal/db/migrations/*.sql` (24) |
| Connection       | Pure-SQLite: WAL, `busy_timeout`, `foreign_keys` pragmas in DSN, `OpenSerialConn` to dodge the single write lock, hand-rolled `migrate()` toggling `PRAGMA foreign_keys` per migration | `internal/db/database.go`                                                                 |
| Full-text search | FTS5: 3 virtual tables + sync triggers + trigram tokenizer + `'rebuild'`, applied outside Atlas at startup; sqlc queries reference the virtual tables directly                         | `internal/db/fts.go`, `schemas/tables/*_fts.sql`                                          |
| Config           | DB path only (`STELLA_HOME`/`stella.db`); no backend selector                                                                                                                          | `internal/config/paths.go`                                                                |

Scale: **59 tables, 371 queries, 24 migrations, ~81 transaction sites.**

## The real difficulty (not "just swap the engine")

A first-pass survey called this "straightforward — change the engine, regenerate." That is wrong.
Four issues, roughly in order of cost:

### 1. Type-affinity divergence (the expensive one)

The codebase is built on SQLite's loose typing:

- Booleans are stored as `INTEGER` → generated as Go `int64` (30+ columns: `enabled`, `is_active`, …).
- Timestamps are stored as `TEXT` via `datetime('now')` → generated as Go `string`, and the app
  parses them as naive UTC by hand (e.g. `parseProjectTime` in `internal/server/projects.go`).

PostgreSQL is strictly typed: those same columns become `boolean` → Go `bool` and
`timestamptz` → Go `time.Time`. **Every generated struct's field types change**, and the ripple
hits every call site that today treats a timestamp as a string or a bool as `int64`. This — not
`?`→`$1` placeholders — is the dominant cost, and it is code-wide.

### 2. sqlc dual codegen produces two incompatible packages

sqlc generates one package per engine. A Postgres build needs a second package
(`pkg/db/sqlcpg`) with its own `Queries` and model types. Even with `emit_interface: true`, each
package's `Querier` references _its own_ models, so Go will not treat `sqlc.Querier` and
`sqlcpg.Querier` as interchangeable. To make the backend runtime-selectable you must introduce a
**hand-written `Store` interface in domain types**, with two implementations behind it — i.e. wrap
all 371 queries. There is no "generate twice and it just works" path.

### 3. FTS5 has no drop-in Postgres equivalent

`fts.go`'s virtual tables + triggers + trigram search must be re-expressed with `tsvector`/`pg_trgm`,
and the search queries (`MATCH` → `@@`/`%`) diverge by dialect — these queries cannot be shared and
must fork per backend.

### 4. Migration + connection layers must be re-implemented in parallel

Postgres needs a real dev database (Atlas can't use in-memory SQLite for it), a separate migration
directory, a `pgx` driver, different pool semantics, and a Postgres branch of `migrate()` (the
`PRAGMA foreign_keys` toggling and the `OpenSerialConn` write-lock workaround are SQLite-only and
disappear).

**Bottom line:** multi-week effort, plus a _permanent ~2× maintenance tax_ — every new query and
schema change must be authored and verified against both dialects forever.

## Options

### A. libSQL / Turso (or rqlite / LiteFS) — keep the SQLite dialect

Distributed/replicated SQLite. **The schema, queries, sqlc setup, and FTS5 stay untouched.**
Delivers replicas, managed cloud, and backups (Turso) with near-zero dialect work.

- ✅ Smallest effort; no 2× tax; satisfies most of the stated _Why_.
- ⚠️ Not "Postgres" — diverges from the issue's literal ask; needs sign-off at #477. Some
  managed-Postgres org requirements (existing PG ops tooling) wouldn't be met.

### B. Real Postgres via a domain-type `Store` interface

Hand-write a `Store` interface in domain types; two implementations (sqlite + pg) behind it.

- ✅ Cleanest long-term, truly backend-agnostic, real Postgres.
- ❌ Largest effort (wrap 371 queries), permanent 2× tax, resolves the type-affinity ripple up front.

### C. Real Postgres, minimum viable

Get only the acceptance-criteria flows (auth/session/agent/task/goal) working on PG; leave FTS and
non-core features SQLite-only or degraded; phase the rest.

- ✅ Meets the issue's acceptance criteria fastest; de-risks incrementally.
- ⚠️ Two backends with _different feature sets_ is its own complexity and a confusing support matrix;
  the type-affinity work can't actually be skipped for the core tables.

## Recommendation

**Validate Option A before committing to B/C.** The issue's needs are ops/deployment needs, and
distributed SQLite (Turso/libSQL) meets them at a fraction of the cost while preserving the entire
SQLite-based stack. Reach for real Postgres (Option B) only if a hard requirement demands the
Postgres dialect or existing managed-PG operations — in which case do B, not C, because the
type-affinity work is unavoidable for core tables anyway and a split feature matrix is worse than a
clean abstraction.

## Open questions for #477/#482

1. Is "PostgreSQL" a hard requirement (org mandate, existing PG ops), or a proxy for
   "replicas + managed cloud + backups"? If the latter, Option A likely wins.
2. Must full-text search work on the non-SQLite backend at parity, or can it be degraded/disabled there?
3. Is a single binary serving either backend required, or are separate builds acceptable?
4. What's the deployment target driving this (specific managed PG? k8s operator? Turso cloud?)?

## Refs

- Parent: #477
- Touch points: `sqlc.yaml`, `internal/db/database.go`, `internal/db/fts.go`,
  `internal/db/schemas/`, `internal/db/migrations/`, `atlas.hcl`,
  `web/content/docs/development/rules/{atlas,sqlc,schema-design}.md`
