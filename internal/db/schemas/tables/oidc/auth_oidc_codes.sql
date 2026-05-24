CREATE TABLE auth_oidc_codes (
    id             TEXT NOT NULL PRIMARY KEY,
    code_hash      TEXT NOT NULL UNIQUE,
    user_id        TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    org_id         TEXT NOT NULL DEFAULT '',
    client_id      TEXT NOT NULL,
    redirect_uri   TEXT NOT NULL,
    scopes         TEXT NOT NULL DEFAULT '[]',
    nonce          TEXT NOT NULL DEFAULT '',
    pkce_challenge TEXT NOT NULL DEFAULT '',
    pkce_method    TEXT NOT NULL DEFAULT 'S256',
    expires_at     TEXT NOT NULL,
    consumed_at    TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_oidc_codes_code_hash ON auth_oidc_codes(code_hash);
CREATE INDEX idx_auth_oidc_codes_user_id ON auth_oidc_codes(user_id);
