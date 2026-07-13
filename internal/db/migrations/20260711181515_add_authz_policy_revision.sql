-- +goose Up
-- Stack 2 / #707 subphase B: PostgreSQL-backed, revision-verified authorization.
--
-- This migration is purely additive. It does NOT touch `auth_policy`, so the
-- legacy `auth.PolicyEngine` (still the only production decision path) keeps
-- byte-identical behaviour. The new typed Authorizer in internal/authz/policy
-- reads only the two tables below; the two engines never share a row, so there
-- is no dual-authoritative decision risk while the new core stays shadow-only.

-- Commit-ordered policy revision counter. A single row (id = 1, enforced by the
-- CHECK + primary key) holds a monotonically increasing revision whose order IS
-- the policy-mutation commit order. A mutation transaction does
-- `UPDATE ... SET revision = revision + 1 ... RETURNING revision`, which takes a
-- row lock on this one row before writing the policy; a second concurrent
-- mutation blocks on that lock until the first commits, so revision order can
-- never omit an earlier-committing transaction. PostgreSQL sequences/`nextval`
-- are deliberately NOT used: sequence allocation order is not commit order and
-- would let a later revision skip an earlier transaction that commits afterward.
CREATE TABLE "authz_policy_revision" (
  "id"         integer PRIMARY KEY DEFAULT 1,
  "revision"   bigint NOT NULL DEFAULT 0,
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT "authz_policy_revision_singleton" CHECK ("id" = 1)
);
INSERT INTO "authz_policy_revision" ("id", "revision") VALUES (1, 0)
  ON CONFLICT ("id") DO NOTHING;

-- Typed custom-policy storage for the new Authorizer. Unlike legacy
-- `auth_policy` (multi-resource/multi-action JSON arrays with wildcards), each
-- row here targets exactly one catalog resource_type + one catalog action with
-- one effect and a typed, schema-validated attribute-predicate set stored as
-- JSON. `status` is the activation lifecycle: only `active` rows are evaluated;
-- `quarantined` rows are inert and carry an operator-readable reason; `inactive`
-- is reserved. `catalog_version` pins the row to a catalog interpretation.
--
-- Enum discipline (schema-design rule): `effect` is a genuinely closed,
-- immutable set, so it keeps a CHECK (mirrors auth_policy). `status`,
-- `resource_type`, and `action` are validated in Go at the write boundary
-- (mutation service) so their value sets can grow without a migration.
CREATE TABLE "authz_policy" (
  "id"                text PRIMARY KEY,
  "name"              text NOT NULL DEFAULT '',
  "resource_type"     text NOT NULL DEFAULT '',
  "action"            text NOT NULL DEFAULT '',
  "effect"            text NOT NULL,
  -- subjects: typed subject selector (which actors the policy applies to) —
  -- kinds/roles/exact grants. Validated in Go at write and revalidated at load.
  -- Legacy backfill leaves it '{}' (an invalid/empty selector) on purpose: those
  -- rows are quarantined and never compiled, so the empty selector is inert.
  "subjects"          jsonb NOT NULL DEFAULT '{}',
  "attributes"        jsonb NOT NULL DEFAULT '{}',
  "catalog_version"   bigint NOT NULL DEFAULT 0,
  "status"            text NOT NULL DEFAULT 'quarantined',
  "quarantine_reason" text NOT NULL DEFAULT '',
  "priority"          bigint NOT NULL DEFAULT 0,
  "created_at"        timestamptz NOT NULL DEFAULT now(),
  "updated_at"        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT "authz_policy_effect_check" CHECK ("effect" = ANY (ARRAY['allow'::text, 'deny'::text]))
);
-- Partial index: the hot read path (`Authorizer` reload) selects only active rows.
CREATE INDEX "idx_authz_policy_active" ON "authz_policy" ("resource_type")
  WHERE "status" = 'active';

-- Backfill: migrate every existing legacy `auth_policy` row into the new table
-- as a QUARANTINED copy with an operator-readable reason. Legacy rows use a
-- multi-resource/wildcard shape that is not interpretable under the typed
-- catalog, so they are never activated by this migration; they surface as
-- diagnosable quarantine records instead of silently vanishing. The legacy id
-- is preserved for traceability; auth_policy itself is left untouched, so the
-- legacy engine still sees the originals. New typed policies (minted by the
-- mutation service) use fresh uuids and never collide with these.
INSERT INTO "authz_policy"
  ("id", "name", "resource_type", "action", "effect", "attributes",
   "catalog_version", "status", "quarantine_reason", "priority", "created_at", "updated_at")
SELECT
  ap."id",
  ap."name",
  '',
  '',
  ap."effect",
  '{}'::jsonb,
  0,
  'quarantined',
  'legacy auth_policy row (catalog_version 0): multi-resource/wildcard shape is not interpretable under the typed authz catalog; quarantined and never evaluated until re-authored for its owning stack',
  ap."priority",
  ap."created_at",
  ap."updated_at"
FROM "auth_policy" AS ap
ON CONFLICT ("id") DO NOTHING;

-- +goose Down
-- Reversible and safe: dropping the two additive tables discards ALL of their
-- rows — the quarantined legacy copies AND any new typed custom policies plus the
-- revision counter created after this migration. That is the intended rollback of
-- the new authorization core. `auth_policy` (the legacy engine's own table) was
-- never modified by this migration, so it and the legacy decision path are
-- unaffected in either direction.
DROP TABLE IF EXISTS "authz_policy";
DROP TABLE IF EXISTS "authz_policy_revision";
