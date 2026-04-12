CREATE TABLE settings_channels (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT '',
    agent_id   TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
