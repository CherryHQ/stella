CREATE TABLE agent_task_acceptance_criterion (
    id TEXT NOT NULL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    description TEXT NOT NULL,
    required BOOLEAN NOT NULL DEFAULT 1,
    position INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(task_id, position)
);

CREATE INDEX idx_agent_task_acceptance_criterion_task_id ON agent_task_acceptance_criterion(task_id);
