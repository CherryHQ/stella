CREATE TABLE ctx_agent_memory (
    user_id     INTEGER NOT NULL,
    agent_id    TEXT NOT NULL REFERENCES settings_agents(id),
    content     TEXT NOT NULL DEFAULT '',
    soul        TEXT NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    constraints TEXT NOT NULL DEFAULT '[]',
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(user_id, agent_id)
);
