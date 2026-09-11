-- +goose Up
-- Multi-replica channel ownership: a live `channel` row already carries the
-- configured intent; these columns record which replica currently owns the
-- poller, the ingress cursor it reached, and the last observed state.
ALTER TABLE "channel"
    -- Replica claiming the right to run this channel's poller. TEXT, not a
    -- FK: replicas are ephemeral processes, not database entities.
    ADD COLUMN runtime_owner_id TEXT,
    -- Fencing token pairing with runtime_lease_until. An owner may only act
    -- (poll, send, write checkpoint) while both are set and the lease is
    -- fresh; a new owner overwriting both implicitly fences the old one.
    ADD COLUMN runtime_token UUID,
    ADD COLUMN runtime_lease_until TIMESTAMPTZ,
    -- Owner-managed durable ingress cursor (platform checkpoints, update
    -- offsets). Read-only to anyone but the token holder.
    ADD COLUMN receive_checkpoint JSONB NOT NULL DEFAULT '{}',
    -- Bumped by every config change. Owners compare config_revision with
    -- applied_revision to detect that they run stale config.
    ADD COLUMN config_revision BIGINT NOT NULL DEFAULT 0,
    -- The revision the current owner last loaded.
    ADD COLUMN applied_revision BIGINT,
    -- Last state the owner reported: stopped/starting/running/draining/error.
    -- Valid values enforced in Go.
    ADD COLUMN runtime_state TEXT NOT NULL DEFAULT 'stopped',
    ADD COLUMN runtime_error_code TEXT,
    ADD COLUMN runtime_observed_at TIMESTAMPTZ;

ALTER TABLE "channel"
    ADD CONSTRAINT channel_runtime_token_lease_pair
        CHECK ((runtime_token IS NULL) = (runtime_lease_until IS NULL)),
    ADD CONSTRAINT channel_revisions_nonnegative
        CHECK (config_revision >= 0 AND (applied_revision IS NULL OR applied_revision >= 0));

-- Replicas scan for channels whose lease expired to take over polling.
CREATE INDEX idx_channel_runtime_lease_until ON "channel" (runtime_lease_until)
    WHERE runtime_lease_until IS NOT NULL;

-- +goose Down
ALTER TABLE "channel"
    DROP CONSTRAINT channel_runtime_token_lease_pair,
    DROP CONSTRAINT channel_revisions_nonnegative;

DROP INDEX idx_channel_runtime_lease_until;

ALTER TABLE "channel"
    DROP COLUMN runtime_owner_id,
    DROP COLUMN runtime_token,
    DROP COLUMN runtime_lease_until,
    DROP COLUMN receive_checkpoint,
    DROP COLUMN config_revision,
    DROP COLUMN applied_revision,
    DROP COLUMN runtime_state,
    DROP COLUMN runtime_error_code,
    DROP COLUMN runtime_observed_at;
