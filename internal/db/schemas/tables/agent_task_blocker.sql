-- Explains why a task is paused. One open blocker per task (D14 / HP2).
-- The transition service merges concurrent blocker conditions into existing
-- detail/event log rather than inserting a second open row.

CREATE TABLE agent_task_blocker (
    id                  TEXT NOT NULL PRIMARY KEY,
    task_id             TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'open',
    question            TEXT NOT NULL DEFAULT '',
    detail              TEXT NOT NULL DEFAULT '{}',
    resolution          TEXT NOT NULL DEFAULT '{}',
    created_by_run_id   TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at         TEXT
);

CREATE INDEX idx_agent_task_blocker_task_open ON agent_task_blocker(task_id) WHERE status='open';
CREATE UNIQUE INDEX uniq_open_blocker_per_task
    ON agent_task_blocker(task_id) WHERE status = 'open';
