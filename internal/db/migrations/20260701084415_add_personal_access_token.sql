-- +goose Up
-- personal_access_token stores user-owned, long-lived, statically-scoped bearer
-- credentials for external/programmatic API access (issue #611, Phase 1).
--
-- It is deliberately separate from auth_user_token (the vault-injected
-- STELLA_TOKEN family): different lifecycle, no auto-generation/rotation, and an
-- explicit least-privilege scope set. Only the SHA-256 hash of the secret is
-- stored -- PATs are high-entropy random tokens, so SHA-256 is sufficient and
-- argon2/bcrypt (password/client-secret hashing) is intentionally not used here.
CREATE TABLE personal_access_token (
    id           UUID PRIMARY KEY DEFAULT uuidv7(),
    public_id    TEXT NOT NULL,
    user_id      UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name         TEXT NOT NULL DEFAULT '',
    token_hash   TEXT NOT NULL,
    last4        TEXT NOT NULL DEFAULT '',
    scopes       TEXT[] NOT NULL DEFAULT '{}',
    -- expires_at is nullable to allow an explicit "no expiry" opt-in; the API
    -- layer default-requires an expiry.
    expires_at   TIMESTAMPTZ NULL,
    last_used_at TIMESTAMPTZ NULL,
    revoked_at   TIMESTAMPTZ NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT personal_access_token_public_id_key UNIQUE (public_id),
    CONSTRAINT personal_access_token_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX idx_personal_access_token_user_id ON personal_access_token (user_id);

-- +goose Down
DROP TABLE personal_access_token;
