CREATE TABLE ctx_agent_memory_snapshot (
    session_id  TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id    TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    version     BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(session_id, user_id, agent_id)
);

CREATE INDEX idx_ctx_agent_memory_snapshot_user_agent ON ctx_agent_memory_snapshot(user_id, agent_id);
