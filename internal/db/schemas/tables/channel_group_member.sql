CREATE TABLE channel_group_member (
    group_id         UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    agent_id         TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    reply_channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(group_id, agent_id)
);
