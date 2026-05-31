CREATE TABLE ctx_agent_memory (
    user_id     TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    content     TEXT NOT NULL DEFAULT '',
    soul        TEXT NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    constraints TEXT NOT NULL DEFAULT '[]',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(user_id, agent_id)
);
