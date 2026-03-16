CREATE TABLE chat_agents (
    platform   TEXT NOT NULL,
    chat_id    TEXT NOT NULL,
    agent_id   TEXT NOT NULL REFERENCES agents(id),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(platform, chat_id)
);
