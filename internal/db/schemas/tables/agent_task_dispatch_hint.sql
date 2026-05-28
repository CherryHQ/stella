-- Persists an explicit executor override between task creation and the next
-- dispatcher tick (B1 / D13). The hint is consumed at claim time by setting
-- consumed_at; live (un-consumed) hints are queried as the first resolution
-- step ahead of session/creator fallback.
--
-- Slice 1: task-only parent, kind='worker' only.
-- Slice 2: widen kind to ('worker','reviewer').
-- Slice 3: widen kind to all four roles and add goal_id with XOR CHECK.

CREATE TABLE agent_task_dispatch_hint (
    id                  TEXT NOT NULL PRIMARY KEY,
    task_id             TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('worker','reviewer')),
    executor_agent_id   TEXT NOT NULL REFERENCES settings_agent(id) ON DELETE CASCADE,
    consumed_at         TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX uniq_active_dispatch_hint_task
    ON agent_task_dispatch_hint(task_id, kind) WHERE consumed_at IS NULL;
