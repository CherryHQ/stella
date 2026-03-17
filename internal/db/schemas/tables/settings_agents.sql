CREATE TABLE settings_agents (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    model         TEXT NOT NULL DEFAULT '',
    model_strong  TEXT NOT NULL DEFAULT '',
    model_fast    TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    workspace     TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
