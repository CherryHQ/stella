CREATE TABLE auth_credential (
    id           TEXT NOT NULL PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    password_hash TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id)
);

CREATE INDEX idx_auth_credential_user_id ON auth_credential(user_id);
