-- +goose Up
-- The bot account that received a platform group message. Reply outbox ops
-- carry this key so the send-time account fence rejects work when the channel
-- was re-bound to a different platform account; credential rotation under the
-- same account identity still owns the pending reply.
ALTER TABLE ctx_group_message
    ADD COLUMN source_account_key TEXT;

-- +goose Down
ALTER TABLE ctx_group_message
    DROP COLUMN source_account_key;
