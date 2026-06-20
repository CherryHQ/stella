CREATE TABLE auth_user_token (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id        UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name           TEXT NOT NULL DEFAULT '',
    token_hash     TEXT NOT NULL UNIQUE,
    token_prefix   TEXT NOT NULL DEFAULT '',
    auto_generated BOOLEAN NOT NULL DEFAULT false,
    last_used_at   TIMESTAMPTZ,
    expires_at     TIMESTAMPTZ,
    rotated_at     TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_auth_user_token_auto_active
ON auth_user_token(user_id)
WHERE auto_generated = true AND revoked_at IS NULL;
