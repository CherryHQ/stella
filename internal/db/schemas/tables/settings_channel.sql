CREATE TABLE settings_channel (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT '',
    agent_id   TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    org_id     TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_settings_channels_org_id ON settings_channel(org_id);
