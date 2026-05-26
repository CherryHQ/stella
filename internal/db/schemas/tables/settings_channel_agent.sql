CREATE TABLE settings_channel_agent (
    channel_id TEXT NOT NULL DEFAULT '',
    platform   TEXT NOT NULL,
    chat_id    TEXT NOT NULL,
    agent_id   TEXT NOT NULL REFERENCES settings_agent(id),
    org_id     TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(channel_id, platform, chat_id)
);

CREATE INDEX idx_settings_channel_agents_org_id ON settings_channel_agent(org_id);
