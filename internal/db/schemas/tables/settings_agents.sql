CREATE TABLE settings_agents (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    model         TEXT NOT NULL DEFAULT '',
    model_strong  TEXT NOT NULL DEFAULT '',
    model_fast    TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    soul          TEXT NOT NULL DEFAULT '',
    workspace     TEXT NOT NULL,
    sandbox       TEXT NOT NULL DEFAULT '{}',
    enabled_builtin_skills TEXT NOT NULL DEFAULT '[]',
    scope         TEXT NOT NULL DEFAULT 'system',
    creator_id    INTEGER NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
