CREATE TABLE ctx_conversations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    channel TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'chat',
    project_id TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    last_active TEXT NOT NULL DEFAULT (datetime('now')),
    bootstrapped_at TEXT,
    agent_id TEXT,
    user_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX idx_one_agent_main
  ON ctx_conversations(agent_id, user_id)
  WHERE kind = 'main' AND project_id IS NULL AND archived = 0;

CREATE UNIQUE INDEX idx_one_project_main
  ON ctx_conversations(project_id)
  WHERE kind = 'main' AND project_id IS NOT NULL AND archived = 0;
