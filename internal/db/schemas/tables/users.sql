CREATE TABLE users (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    external_id      TEXT NOT NULL,
    platform         TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    default_agent_id TEXT REFERENCES agents(id),
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(external_id, platform)
);
