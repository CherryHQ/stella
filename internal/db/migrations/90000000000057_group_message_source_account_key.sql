-- +goose Up
-- The bot account that received a platform group message. Audit-only, like
-- source_channel_id: a shared trigger can wake members replying through other
-- channels, so reply ops fence on the reply channel's account snapshot
-- (channel.runtime_account_key), not this observing account.
ALTER TABLE ctx_group_message
    ADD COLUMN source_account_key TEXT;

-- +goose Down
ALTER TABLE ctx_group_message
    DROP COLUMN source_account_key;
