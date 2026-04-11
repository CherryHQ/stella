CREATE TABLE settings_channel_agents (
    channel_id TEXT NOT NULL DEFAULT '',
    platform   TEXT NOT NULL,
    chat_id    TEXT NOT NULL,
    agent_id   TEXT NOT NULL REFERENCES settings_agents(id),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(channel_id, platform, chat_id)
);
