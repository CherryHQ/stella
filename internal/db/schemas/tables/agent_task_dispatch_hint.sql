-- Persists an explicit executor override between task/goal creation and
-- the next dispatcher tick (B1 / D13). Consumed at claim time.
--
-- Slice 1 introduced this for tasks/workers; Slice 2 widened kind to
-- include 'reviewer'; Slice 3 adds goal-parented rows and widens kind
-- with planner/synthesizer.

CREATE TABLE agent_task_dispatch_hint (
    id                  TEXT NOT NULL PRIMARY KEY,
    task_id             TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    goal_id             TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL,
    executor_agent_id   TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    consumed_at         TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
);

CREATE UNIQUE INDEX uniq_active_dispatch_hint_task
    ON agent_task_dispatch_hint(task_id, kind) WHERE task_id IS NOT NULL AND consumed_at IS NULL;
CREATE UNIQUE INDEX uniq_active_dispatch_hint_goal
    ON agent_task_dispatch_hint(goal_id, kind) WHERE goal_id IS NOT NULL AND consumed_at IS NULL;
