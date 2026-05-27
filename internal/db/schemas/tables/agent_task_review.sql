CREATE TABLE agent_task_review (
    id TEXT NOT NULL PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    reviewer_type TEXT NOT NULL CHECK (reviewer_type IN ('agent','human','system')),
    reviewer_id TEXT NOT NULL DEFAULT '',
    submitted_run_id TEXT NOT NULL REFERENCES agent_task_run(id) ON DELETE CASCADE,
    reviewer_run_id TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'requested' CHECK (status IN ('requested','approved','changes_requested','rejected','cancelled')),
    summary TEXT NOT NULL DEFAULT '',
    feedback TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at TEXT
);

CREATE INDEX idx_agent_task_review_task_id ON agent_task_review(task_id);
CREATE INDEX idx_agent_task_review_status ON agent_task_review(status);
CREATE INDEX idx_agent_task_review_submitted_run_id ON agent_task_review(submitted_run_id);
