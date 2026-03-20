CREATE TABLE auth_identities (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    linked_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, external_id)
);
