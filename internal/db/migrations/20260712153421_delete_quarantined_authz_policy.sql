-- +goose Up
-- #712 Item 5: reconcile policy activation by removing the quarantined inert rows.
--
-- The 20260711181515 migration backfilled every legacy `auth_policy` row into
-- `authz_policy` as `status='quarantined'`: inert copies that the Authorizer
-- never evaluates (only `active` rows are compiled). Stack 7 is the FINAL stack,
-- so no future owner will ever re-author these legacy shapes into active typed
-- policies — they would sit inert forever. Delete them now so the only rows left
-- in `authz_policy` are deliberate typed custom policies authored against the
-- catalog, with no dead quarantine residue to reason about.
--
-- The physical legacy `auth_policy` table is intentionally left in place: Part A
-- of this item already severed all code access (its sqlc query file and the
-- AuthStore CRUD are gone), so the table is dead but harmless, and dropping it is
-- a separate concern kept out of this data-reconciliation migration.
DELETE FROM "authz_policy" WHERE "status" = 'quarantined';

-- +goose Down
-- Intentional no-op: the deleted rows were inert quarantined COPIES of legacy
-- `auth_policy` rows (never evaluated, never a decision source), so their removal
-- has no behavioural effect to reverse. The originals still live in the untouched
-- `auth_policy` table; re-running 20260711181515's backfill is the only path that
-- ever created them, and it is not part of this migration. This deletion is
-- deliberately irreversible.
SELECT 1;
