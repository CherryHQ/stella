CREATE TABLE auth_invite (
    id          TEXT NOT NULL PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,
    org_id      TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    email       TEXT,
    role        TEXT NOT NULL DEFAULT 'user',
    status      TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','revoked')),
    max_uses    INTEGER NOT NULL DEFAULT 1,
    use_count   INTEGER NOT NULL DEFAULT 0,
    invited_by  TEXT NOT NULL REFERENCES auth_user(id),
    accepted_by TEXT REFERENCES auth_user(id),
    expires_at  TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_invite_org ON auth_invite(org_id);
CREATE INDEX idx_auth_invite_token_hash ON auth_invite(token_hash);
