CREATE TABLE auth_user_tokens (
    id             TEXT PRIMARY KEY,
    user_id        INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL DEFAULT '',
    token_hash     TEXT NOT NULL UNIQUE,
    token_prefix   TEXT NOT NULL DEFAULT '',
    auto_generated INTEGER NOT NULL DEFAULT 0,
    last_used_at   TEXT,
    expires_at     TEXT,
    rotated_at     TEXT,
    revoked_at     TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_auth_user_tokens_auto_active
ON auth_user_tokens(user_id)
WHERE auto_generated = 1 AND revoked_at IS NULL;
