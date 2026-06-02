CREATE TABLE channel (
    id         TEXT NOT NULL PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    type       TEXT NOT NULL DEFAULT '',
    agent_id   TEXT REFERENCES agent(id) ON DELETE SET NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
