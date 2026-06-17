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
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'draft',
    priority            TEXT NOT NULL DEFAULT 'routine',
    review_policy       TEXT NOT NULL DEFAULT 'none',
    active_review_id    TEXT,  -- FK added in agent_review.sql (cycle)
    required            BOOLEAN NOT NULL DEFAULT true,
    retry_count         BIGINT NOT NULL DEFAULT 0,
    max_retries         BIGINT NOT NULL DEFAULT 3,
    not_before          TIMESTAMPTZ,
    deadline_at         TIMESTAMPTZ,
    active_run_id       TEXT,  -- FK added in agent_task_run.sql (cycle)
    active_blocker_id   TEXT,  -- FK added in agent_task_blocker.sql (cycle)
    context             TEXT NOT NULL DEFAULT '{}',
    output              TEXT NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ
);

CREATE UNIQUE INDEX uniq_agent_task_session ON agent_task(session_id);
CREATE INDEX idx_agent_task_agent_status_not_before ON agent_task(agent_id, status, not_before);
CREATE INDEX idx_agent_task_user_agent        ON agent_task(user_id, agent_id);
CREATE INDEX idx_agent_task_user_created      ON agent_task(user_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_user_agent_status_created ON agent_task(user_id, agent_id, status, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_user_agent_project_created ON agent_task(user_id, agent_id, project_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_task_ready_candidates
    ON agent_task(priority DESC, created_at ASC)
    WHERE status = 'ready' AND active_run_id IS NULL;
CREATE INDEX idx_agent_task_session           ON agent_task(session_id);
CREATE INDEX idx_agent_task_goal              ON agent_task(goal_id);
CREATE INDEX idx_agent_task_project           ON agent_task(project_id);
