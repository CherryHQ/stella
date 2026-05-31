-- Slice 2: review pipeline.
-- XOR parent (task | goal) from day one even though Slice 2 only writes task-
-- parented rows; Slice 3 starts inserting goal-parented rows. See plan.md
-- (D8 / B-final).

CREATE TABLE agent_review (
    id                          TEXT NOT NULL PRIMARY KEY,
    task_id                     TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    goal_id                     TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,
    submitted_run_id            TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    reviewer_run_id             TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    reviewer_type               TEXT NOT NULL,
    reviewer_user_id            TEXT REFERENCES auth_user(id) ON DELETE SET NULL,
    escalated_from_review_id    TEXT REFERENCES agent_review(id) ON DELETE SET NULL,
    status                      TEXT NOT NULL DEFAULT 'requested',
    summary                     TEXT NOT NULL DEFAULT '',
    feedback                    TEXT NOT NULL DEFAULT '',
    created_at                  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                  TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at                 TEXT,
    CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL)
      OR
      (task_id IS NULL AND goal_id IS NOT NULL)
    )
);

CREATE INDEX idx_agent_review_task ON agent_review(task_id, created_at DESC);
CREATE INDEX idx_agent_review_open ON agent_review(status) WHERE status IN ('requested','in_progress');
CREATE UNIQUE INDEX uniq_open_review_per_task
    ON agent_review(task_id) WHERE task_id IS NOT NULL AND status IN ('requested','in_progress');
CREATE UNIQUE INDEX uniq_open_review_per_goal
    ON agent_review(goal_id) WHERE goal_id IS NOT NULL AND status IN ('requested','in_progress');
