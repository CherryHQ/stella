CREATE TABLE project (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    base_dir TEXT NOT NULL,
    description TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(agent_id, user_id, name)
);
