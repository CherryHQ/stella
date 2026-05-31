CREATE TABLE auth_oidc_access_token (
    id         TEXT NOT NULL PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    client_id  TEXT NOT NULL,
    scopes     TEXT NOT NULL DEFAULT '[]',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_oidc_access_token_token_hash ON auth_oidc_access_token(token_hash);
CREATE INDEX idx_auth_oidc_access_token_user_id ON auth_oidc_access_token(user_id);
