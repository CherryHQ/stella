-- +goose Up
-- The platform account identity the current lease owner's adapter registered
-- for this channel. Reply outbox ops snapshot this key at dispatch-accept
-- time so the send-time account fence follows the responding channel's
-- account, not the observing trigger's. Kept after lease release: a stale
-- last-known key rejects pending ops under a re-bound account, which is the
-- safe direction. Overwritten by the next owner's registration.
ALTER TABLE channel
    ADD COLUMN runtime_account_key TEXT;

-- +goose Down
ALTER TABLE channel
    DROP COLUMN runtime_account_key;
