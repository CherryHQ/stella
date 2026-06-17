-- Slice 3: high-level objective container.
-- A goal is a row, not a conversation (D12). Planning + synthesis happen as
-- runs in agent_task_run with kind='planner'/'synthesizer'; their sessions
-- live on the runs themselves.

CREATE TABLE agent_goal (
    id              TEXT NOT NULL PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id        TEXT NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
    project_id      TEXT REFERENCES project(id) ON DELETE SET NULL,
    title           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'draft',
    priority        TEXT NOT NULL DEFAULT 'routine',
    review_policy   TEXT NOT NULL DEFAULT 'none',
    active_review_id TEXT,  -- FK added in agent_review.sql (cycle: agent_goal <-> agent_review)
    context         TEXT NOT NULL DEFAULT '{}',
    output          TEXT NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    cancelled_at    TIMESTAMPTZ
);

CREATE INDEX idx_agent_goal_agent_project ON agent_goal(agent_id, project_id);
CREATE INDEX idx_agent_goal_project       ON agent_goal(project_id);
CREATE INDEX idx_agent_goal_user_created  ON agent_goal(user_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_goal_planning_candidates
    ON agent_goal(priority DESC, created_at ASC)
    WHERE status = 'draft';
CREATE INDEX idx_agent_goal_synthesis_candidates
    ON agent_goal(priority DESC, updated_at ASC)
    WHERE status = 'running' AND review_policy != 'none';
