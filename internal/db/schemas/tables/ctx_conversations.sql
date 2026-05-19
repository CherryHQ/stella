CREATE TABLE ctx_conversations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    channel TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'chat',
    project_id TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    last_active TEXT NOT NULL DEFAULT (datetime('now')),
    bootstrapped_at TEXT,
    agent_id TEXT,
    user_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (source != 'main' OR project_id IS NOT NULL)
);

CREATE UNIQUE INDEX idx_one_main_per_project
  ON ctx_conversations(project_id)
  WHERE source = 'main' AND archived = 0 AND project_id IS NOT NULL;
