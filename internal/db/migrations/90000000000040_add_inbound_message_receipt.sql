-- +goose Up
-- A short-lived receipt makes platform retries idempotent across a process
-- restart. This is deliberately separate from channel_chat_command_receipt:
-- that table protects destructive commands forever, while ordinary turns need
-- retry suppression only for the platform's delivery window.
SET LOCAL lock_timeout = '10s';

CREATE TABLE channel_inbound_message_receipt (
    id         UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL,
    chat_key   TEXT NOT NULL,
    message_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, chat_key, message_id)
);

CREATE INDEX idx_channel_inbound_message_receipt_expires_at
ON channel_inbound_message_receipt (expires_at);

-- +goose Down
DROP TABLE channel_inbound_message_receipt;
