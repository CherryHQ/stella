-- Append-only audit log. Every transition through the transition service
-- writes one row. Slice 1 fields only; Slice 2 adds review_id, Slice 3 adds
-- goal_id and widens actor_type.

CREATE TABLE agent_task_event (
    id              TEXT NOT NULL PRIMARY KEY,
    task_id         TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    goal_id         TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,
    run_id          TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    blocker_id      TEXT REFERENCES agent_task_blocker(id) ON DELETE SET NULL,
    review_id       TEXT REFERENCES agent_review(id) ON DELETE SET NULL,
    event_type      TEXT NOT NULL,
    from_status     TEXT,
    to_status       TEXT,
    actor_type      TEXT NOT NULL,
    actor_id        TEXT,
    detail          TEXT NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_task_event_task ON agent_task_event(task_id, created_at DESC);
CREATE INDEX idx_agent_task_event_goal ON agent_task_event(goal_id, created_at DESC);
CREATE INDEX idx_agent_task_event_run  ON agent_task_event(run_id);
