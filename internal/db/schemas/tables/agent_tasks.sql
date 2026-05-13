CREATE TABLE agent_tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','blocked','review_requested','done','failed','cancelled')),
    priority TEXT NOT NULL DEFAULT 'routine' CHECK(priority IN ('routine','urgent')),
    session_id TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '{}',
    review_request TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    user_id INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_tasks_status ON agent_tasks(status);
CREATE INDEX idx_agent_tasks_user_id ON agent_tasks(user_id);
