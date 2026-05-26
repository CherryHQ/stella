CREATE TABLE auth_user (
    id                  TEXT NOT NULL PRIMARY KEY,
    email               TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL DEFAULT '',
    avatar_url          TEXT NOT NULL DEFAULT '',
    default_agent_id    TEXT REFERENCES settings_agent(id),
    notify_identity_id  TEXT REFERENCES auth_channel_identity(id) ON DELETE SET NULL,
    age_public_key      TEXT NOT NULL DEFAULT '',
    age_private_key     TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_auth_user_email ON auth_user(email);
