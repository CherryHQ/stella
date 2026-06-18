-- Slice 1 (MVP) schema for agent_task.
-- Status describes business lifecycle only; readiness is computed.
-- See plan.md and /Users/vaayne/.agents/sessions/stella/2026-05-28-task-system-v2/plan.md.

CREATE TABLE agent_task (
    id                  TEXT NOT NULL PRIMARY KEY,
    user_id             TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,  -- owner/manager agent context
    session_id          TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT, -- durable worker session
    goal_id             TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,        -- Slice 3
    project_id          TEXT REFERENCES project(id) ON DELETE SET NULL,
    source_plan_id      TEXT REFERENCES agent_goal_plan(id) ON DELETE SET NULL,  -- #525: materialized from this plan
    plan_item_id        TEXT NOT NULL DEFAULT '',                                 -- #525: plan item this task realizes
    detached_at         TEXT,                                                     -- #525: replan removed the item but task has output
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
    active_run_id       TEXT REFERENCES agent_task_run(id) ON DELETE RESTRICT,
    active_blocker_id   TEXT REFERENCES agent_task_blocker(id) ON DELETE RESTRICT,
    context             TEXT NOT NULL DEFAULT '{}',
    output              TEXT NOT NULL DEFAULT '{}',
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at        TEXT,
    cancelled_at        TEXT,
    archived_at         TEXT
);

CREATE UNIQUE INDEX uniq_agent_task_session ON agent_task(session_id);
CREATE INDEX idx_agent_task_agent_status_not_before ON agent_task(agent_id, status, not_before);
CREATE INDEX idx_agent_task_user_agent        ON agent_task(user_id, agent_id);
CREATE INDEX idx_agent_task_user_created      ON agent_task(user_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_user_archived_created ON agent_task(user_id, archived_at, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_user_agent_status_created ON agent_task(user_id, agent_id, status, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_user_agent_project_created ON agent_task(user_id, agent_id, project_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_ready_candidates
    ON agent_task(priority DESC, created_at ASC)
    WHERE status = 'ready' AND active_run_id IS NULL;
CREATE INDEX idx_agent_task_session           ON agent_task(session_id);
CREATE INDEX idx_agent_task_goal              ON agent_task(goal_id);
CREATE INDEX idx_agent_task_project           ON agent_task(project_id);
-- #525: one task per (plan, plan item); the materializer Gets-then-creates by this key.
CREATE UNIQUE INDEX uniq_agent_task_source_plan_item
    ON agent_task(source_plan_id, plan_item_id)
    WHERE source_plan_id IS NOT NULL AND plan_item_id != '';
