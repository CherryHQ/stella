CREATE TABLE agent_task_event (
    id TEXT NOT NULL PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    run_id TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    review_id TEXT REFERENCES agent_task_review(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_task_event_task_id ON agent_task_event(task_id, created_at DESC);
