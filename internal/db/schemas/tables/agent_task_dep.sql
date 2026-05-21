CREATE TABLE agent_task_dep (
    task_id TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    dep_id  TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (task_id, dep_id),
    CHECK (task_id != dep_id)
);

CREATE INDEX idx_agent_task_dep_dep_id ON agent_task_dep(dep_id);
