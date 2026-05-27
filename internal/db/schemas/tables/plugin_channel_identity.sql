CREATE TABLE plugin_channel_identity (
    id          TEXT NOT NULL PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, external_id)
);

CREATE INDEX idx_channel_identity_user_id ON plugin_channel_identity(user_id);
