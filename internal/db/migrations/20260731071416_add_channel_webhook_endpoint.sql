-- +goose Up
CREATE TABLE channel_webhook_endpoint (
    id                         UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id                 TEXT NOT NULL UNIQUE REFERENCES channel(id) ON DELETE CASCADE,
    owner_user_id              UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    provider                   TEXT NOT NULL,
    token_public_id            TEXT NOT NULL UNIQUE,
    token_hash                 TEXT NOT NULL,
    token_last4                TEXT NOT NULL,
    provider_secret_ciphertext TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at                 TIMESTAMPTZ,
    CONSTRAINT channel_webhook_endpoint_provider_secret_coupling CHECK (
        (provider = 'github' AND provider_secret_ciphertext <> '')
        OR (provider <> 'github' AND provider_secret_ciphertext = '')
    )
);
CREATE INDEX idx_channel_webhook_endpoint_owner_user_id
    ON channel_webhook_endpoint(owner_user_id);

CREATE TABLE channel_webhook_delivery (
    id          UUID PRIMARY KEY DEFAULT uuidv7(),
    endpoint_id UUID NOT NULL REFERENCES channel_webhook_endpoint(id) ON DELETE CASCADE,
    provider    TEXT NOT NULL,
    delivery_id TEXT NOT NULL CHECK (length(delivery_id) <= 256),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, provider, delivery_id)
);
CREATE INDEX idx_channel_webhook_delivery_created_at
    ON channel_webhook_delivery(created_at);

-- +goose Down
DROP TABLE channel_webhook_delivery;
DROP TABLE channel_webhook_endpoint;
