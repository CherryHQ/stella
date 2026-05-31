-- Slice 1 (MVP) schema for agent_task.
-- Status describes business lifecycle only; readiness is computed.
-- See plan.md and /Users/vaayne/.agents/sessions/stella/2026-05-28-task-system-v2/plan.md.

CREATE TABLE agent_task (
    id                  TEXT NOT NULL PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT REFERENCES agent(id) ON DELETE SET NULL,  -- D12: creator, NOT assignee
    goal_id             TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,        -- Slice 3
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'draft',
    priority            TEXT NOT NULL DEFAULT 'routine',
    review_policy       TEXT NOT NULL DEFAULT 'none',
    active_review_id    TEXT REFERENCES agent_review(id) ON DELETE RESTRICT,
    required            INTEGER NOT NULL DEFAULT 1,
    retry_count         INTEGER NOT NULL DEFAULT 0,
    max_retries         INTEGER NOT NULL DEFAULT 3,
    not_before          TEXT,
    deadline_at         TEXT,
    session_id          TEXT,                                                    -- D12: null  next run mints fresh
    active_run_id       TEXT REFERENCES agent_task_run(id) ON DELETE RESTRICT,
    active_blocker_id   TEXT REFERENCES agent_task_blocker(id) ON DELETE RESTRICT,
    context             TEXT NOT NULL DEFAULT '{}',
    output              TEXT NOT NULL DEFAULT '{}',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at        TEXT,
    cancelled_at        TEXT
);

CREATE INDEX idx_agent_task_status_not_before ON agent_task(status, not_before);
CREATE INDEX idx_agent_task_session           ON agent_task(session_id);
CREATE INDEX idx_agent_task_goal              ON agent_task(goal_id);
