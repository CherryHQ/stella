-- +goose Up
-- Probe health state and the persisted tool catalog live on the registration
-- row itself: the catalog is a snapshot of the remote tools/list, replaced
-- wholesale on every probe. Valid status values (unknown, ok, error,
-- needs_auth) and credential modes (shared, per_user) are enforced in Go, not
-- by a CHECK, so they can grow without a migration.
ALTER TABLE "mcp_server"
    ADD COLUMN "status" TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN "status_error" TEXT NOT NULL DEFAULT '',
    ADD COLUMN "probed_at" TIMESTAMPTZ,
    ADD COLUMN "tools" JSONB NOT NULL DEFAULT '[]',
    ADD COLUMN "credential_mode" TEXT NOT NULL DEFAULT 'shared';

-- +goose Down
ALTER TABLE "mcp_server"
    DROP COLUMN "credential_mode",
    DROP COLUMN "tools",
    DROP COLUMN "probed_at",
    DROP COLUMN "status_error",
    DROP COLUMN "status";
