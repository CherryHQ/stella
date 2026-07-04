-- +goose Up
CREATE TABLE email_send_dedup (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX idx_email_send_dedup_sent_at ON email_send_dedup (sent_at);

-- +goose Down
DROP TABLE email_send_dedup;
