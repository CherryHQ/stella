-- +goose Up
-- Writer-exit evidence for session takeover: a claimant may only overwrite an
-- expired lease when the previous owner is confirmed dead or the row is
-- owner-less. An alive or unverifiable owner keeps the session unavailable
-- rather than letting two writers share one workspace (plan D9).
ALTER TABLE ctx_session_execution
    ADD COLUMN owner_id TEXT,
    ADD COLUMN owner_host TEXT,
    ADD COLUMN owner_pid INTEGER;

-- +goose Down
ALTER TABLE ctx_session_execution
    DROP COLUMN owner_id,
    DROP COLUMN owner_host,
    DROP COLUMN owner_pid;
