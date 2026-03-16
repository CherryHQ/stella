CREATE TABLE user_agent_memory (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    agent_id   TEXT NOT NULL REFERENCES agents(id),
    content    TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(user_id, agent_id)
);
