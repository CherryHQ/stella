CREATE TABLE auth_users (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    username            TEXT UNIQUE NOT NULL,
    password_hash       TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT 'user',
    is_active           INTEGER NOT NULL DEFAULT 1,
    default_agent_id    TEXT REFERENCES settings_agents(id),
    notify_identity_id  TEXT REFERENCES auth_identities(id) ON DELETE SET NULL,
    age_public_key      TEXT NOT NULL DEFAULT '',
    age_private_key     TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
