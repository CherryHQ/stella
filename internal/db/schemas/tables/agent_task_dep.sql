-- DAG edges between agent_task rows.
-- Cycle prevention is enforced in the transition service (DFS on AddDep), not the DB.
-- Waiver fields land in Slice 1 even though full soft-dep semantics ship in Slice 5.

CREATE TABLE agent_task_dep (
    task_id         TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    dep_task_id     TEXT NOT NULL REFERENCES agent_task(id) ON DELETE CASCADE,
    dep_kind        TEXT NOT NULL DEFAULT 'hard' CHECK (dep_kind IN ('hard','soft')),
    on_failure      TEXT NOT NULL DEFAULT 'block' CHECK (on_failure IN ('block','fail','ignore')),
    waived_at       TEXT,
    waived_by_user  TEXT REFERENCES auth_user(id) ON DELETE SET NULL,
    waiver_reason   TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (task_id, dep_task_id),
    CHECK (task_id != dep_task_id)
);

CREATE INDEX idx_agent_task_dep_dep ON agent_task_dep(dep_task_id);
