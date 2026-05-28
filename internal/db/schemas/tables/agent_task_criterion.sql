-- Per-task acceptance criteria, evaluated by reviewers. Slice 2.

CREATE TABLE agent_task_criterion (
    id              TEXT NOT NULL PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    description     TEXT NOT NULL,
    required_flag   INTEGER NOT NULL DEFAULT 1,
    position        INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_task_criterion_task ON agent_task_criterion(task_id, position);
