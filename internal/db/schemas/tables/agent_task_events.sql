CREATE TABLE agent_task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK(event_type IN ('started','progress','blocked','review_requested','done','failed','cancelled')),
    detail TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_task_events_task_id ON agent_task_events(task_id, created_at DESC);
