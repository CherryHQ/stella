CREATE TABLE auth_membership (
    id              TEXT NOT NULL PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    organization_id TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    role            TEXT NOT NULL DEFAULT 'user',
    is_active       INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, organization_id)
);

CREATE INDEX idx_auth_membership_user_id ON auth_membership(user_id);
CREATE INDEX idx_auth_membership_organization_id ON auth_membership(organization_id);
