CREATE TABLE channel_agent (
    channel_id TEXT NOT NULL DEFAULT '',
    platform   TEXT NOT NULL,
    chat_id    TEXT NOT NULL,
    agent_id   TEXT NOT NULL REFERENCES agent(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(channel_id, platform, chat_id)
);
