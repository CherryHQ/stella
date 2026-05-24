CREATE TABLE auth_oidc_access_tokens (
    id         TEXT NOT NULL PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL DEFAULT '',
    client_id  TEXT NOT NULL,
    scopes     TEXT NOT NULL DEFAULT '[]',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_oidc_access_tokens_token_hash ON auth_oidc_access_tokens(token_hash);
CREATE INDEX idx_auth_oidc_access_tokens_user_id ON auth_oidc_access_tokens(user_id);
