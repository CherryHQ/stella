CREATE TABLE ctx_agent_memory_snapshot (
    session_id  TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(session_id, user_id, agent_id)
);

CREATE INDEX idx_memory_snapshots_user_agent ON ctx_agent_memory_snapshot(user_id, agent_id);
