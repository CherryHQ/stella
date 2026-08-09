-- +goose Up
-- PostgreSQL 11+ stores a constant column default in the catalog, so this is
-- O(1): legacy rows read as human without a table rewrite or validation scan.
ALTER TABLE ctx_message
    ADD COLUMN actor_type TEXT NOT NULL DEFAULT 'human',
    ADD COLUMN actor_id TEXT,
    ADD COLUMN source_session_id TEXT;

-- +goose Down
ALTER TABLE ctx_message
    DROP COLUMN source_session_id,
    DROP COLUMN actor_id,
    DROP COLUMN actor_type;
