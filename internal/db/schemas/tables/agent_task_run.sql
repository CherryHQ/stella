CREATE TABLE agent_task_run (
    id TEXT NOT NULL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,
    kind TEXT NOT NULL CHECK (kind IN ('manager_run','worker_run','reviewer_run')),
    purpose TEXT NOT NULL CHECK (purpose IN ('planning','synthesis','replan','execution','review','auto_approval','failure_assessment')),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','failed','cancelled','interrupted')),
    session_id TEXT,
    result_json TEXT NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    deadline_at TEXT,
    started_at TEXT,
    finished_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_task_run_user_id ON agent_task_run(user_id);
CREATE INDEX idx_agent_task_run_task_kind_status ON agent_task_run(task_id, kind, status);
CREATE INDEX idx_agent_task_run_agent_id ON agent_task_run(agent_id);
CREATE INDEX idx_agent_task_run_status ON agent_task_run(status);
