CREATE TABLE agent_task (
    id TEXT NOT NULL PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    priority TEXT NOT NULL DEFAULT 'routine',
    session_id TEXT REFERENCES auth_sessions(id) ON DELETE SET NULL,
    context TEXT NOT NULL DEFAULT '{}',
    review_request TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES settings_agents(id) ON DELETE SET NULL,
    user_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_task_user_id_status ON agent_task(user_id, status);
CREATE INDEX idx_agent_task_status ON agent_task(status);
CREATE INDEX idx_agent_task_session_id ON agent_task(session_id);
CREATE INDEX idx_agent_task_agent_id ON agent_task(agent_id);
