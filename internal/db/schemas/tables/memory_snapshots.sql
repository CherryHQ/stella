CREATE TABLE memory_snapshots (
    session_id  TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    agent_id    TEXT NOT NULL,
    version     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(session_id, user_id, agent_id)
);

CREATE INDEX idx_memory_snapshots_user_agent ON memory_snapshots(user_id, agent_id);
