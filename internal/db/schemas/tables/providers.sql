CREATE TABLE providers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    api_key    TEXT NOT NULL DEFAULT '',
    base_url   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
