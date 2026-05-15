CREATE TABLE auth_identities (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    linked_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, external_id)
);
