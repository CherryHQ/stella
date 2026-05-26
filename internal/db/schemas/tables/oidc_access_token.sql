CREATE TABLE oidc_access_token (
    id         TEXT NOT NULL PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    user_id    TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    org_id     TEXT NOT NULL DEFAULT '',
    client_id  TEXT NOT NULL,
    scopes     TEXT NOT NULL DEFAULT '[]',
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_oidc_access_tokens_token_hash ON oidc_access_token(token_hash);
CREATE INDEX idx_auth_oidc_access_tokens_user_id ON oidc_access_token(user_id);
