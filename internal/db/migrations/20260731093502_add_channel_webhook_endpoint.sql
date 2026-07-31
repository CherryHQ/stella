-- +goose Up
-- channel_webhook_endpoint is the one-to-one capability facet of a webhook
-- channel. The channel FK is the primary key: the endpoint has no independent
-- identity. Parent deletes are RESTRICT so an active capability must be revoked
-- before the channel or its owner can be hard-deleted; nothing bypasses the
-- lifecycle fence via a cascade. Provider is a bounded, Go-validated value
-- (only 'generic' is accepted in this core layer). Token verifier material is
-- stored hash-only; plaintext is disclosed once at issuance and never persisted.
CREATE TABLE channel_webhook_endpoint (
    channel_id      TEXT PRIMARY KEY REFERENCES channel(id) ON DELETE RESTRICT,
    owner_user_id   UUID NOT NULL REFERENCES auth_user(id) ON DELETE RESTRICT,
    provider        TEXT NOT NULL CHECK (length(provider) <= 32),
    token_public_id TEXT NOT NULL UNIQUE,
    token_hash      TEXT NOT NULL,
    token_last4     TEXT NOT NULL,
    revision        BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at      TIMESTAMPTZ
);
-- Index the owner foreign key: supports owner reference/delete checks and the
-- owner-scoped lookups that the lifecycle rules perform.
CREATE INDEX idx_channel_webhook_endpoint_owner_user_id
    ON channel_webhook_endpoint (owner_user_id);

-- +goose Down
DROP TABLE channel_webhook_endpoint;
