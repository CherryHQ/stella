-- +goose Up
-- webhook is a personal invocation capability. Its opaque credential is stored
-- hash-only; plaintext is disclosed only by Create and Rotate responses.
CREATE TABLE webhook (
    id                      UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id                 UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id                TEXT NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
    name                    TEXT NOT NULL,
    provider                TEXT NOT NULL CHECK (length(provider) <= 32),
    is_enabled              BOOLEAN NOT NULL DEFAULT true,
    wait_timeout_seconds    INTEGER NOT NULL DEFAULT 60 CHECK (wait_timeout_seconds > 0 AND wait_timeout_seconds <= 600),
    max_run_timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (max_run_timeout_seconds > 0 AND max_run_timeout_seconds <= 3600),
    token_public_id         TEXT NOT NULL UNIQUE,
    token_hash              TEXT NOT NULL,
    token_last4             TEXT NOT NULL CHECK (length(token_last4) = 4),
    revision                BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at              TIMESTAMPTZ
);
-- Supports the owner-scoped list query without sorting the user's full set.
-- user_id remains the leading column, so this also indexes the foreign key.
CREATE INDEX idx_webhook_user_id_created_at_id
    ON webhook (user_id, created_at DESC, id DESC);
CREATE INDEX idx_webhook_agent_id ON webhook (agent_id);

-- +goose Down
DROP TABLE webhook;
