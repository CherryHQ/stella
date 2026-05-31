CREATE TABLE auth_policy (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    effect     TEXT NOT NULL CHECK(effect IN ('allow', 'deny')),
    subjects   TEXT NOT NULL DEFAULT '{}',
    actions    TEXT NOT NULL DEFAULT '[]',
    resources  TEXT NOT NULL DEFAULT '[]',
    conditions TEXT NOT NULL DEFAULT '{}',
    priority   INTEGER NOT NULL DEFAULT 0,
    is_system  INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
