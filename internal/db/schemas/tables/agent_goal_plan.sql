-- Issue #525: the accepted-plan gate. One plan row per goal (goal_id UNIQUE),
-- edited in place — no versioning, no supersede chain. A goal reaches 'planned'
-- (and may be activated) only once its plan has been accepted AND materialized
-- into work tasks; work tasks come solely from the materializer, never manual
-- CreateTask. See plan.md D1/D2/D6.
--
-- Two content slots: content_json is the last materialized content (written only
-- inside MaterializeGoalPlan's tx); pending_content_json is the in-flight edit
-- (create draft or replan). Promote pending -> content happens only at materialize
-- so a running goal keeps executing the accepted content while an edit is staged.

CREATE TABLE agent_goal_plan (
    id                   TEXT NOT NULL PRIMARY KEY,
    goal_id              TEXT NOT NULL UNIQUE REFERENCES agent_goal(id) ON DELETE CASCADE,
    status               TEXT NOT NULL DEFAULT 'draft',  -- valid values enforced in Go
    review_policy        TEXT NOT NULL DEFAULT 'none',   -- valid values enforced in Go
    content_json         TEXT NOT NULL DEFAULT '{}',     -- last materialized content
    pending_content_json TEXT,                           -- in-flight edit; NULL when none
    source_run_id        TEXT REFERENCES agent_task_run(id) ON DELETE SET NULL,
    approved_review_id   TEXT REFERENCES agent_review(id) ON DELETE SET NULL,
    -- The dedicated planning session a deferred goal is planned in: an agent
    -- delegates planning into this child session, the user re-opens it from the
    -- UI to refine the plan by chatting. References ctx_conversation.session_id
    -- (the app-facing session id minters return), not the surrogate id PK. SET
    -- NULL if the session is purged.
    planning_session_id  TEXT REFERENCES ctx_conversation(session_id) ON DELETE SET NULL,
    accepted_at          TEXT,
    materialized_at      TEXT,
    created_at           TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_goal_plan_goal_status ON agent_goal_plan(goal_id, status);
-- Index every FK (schema rule): these back the ON DELETE SET NULL scans when a
-- source run or approved review is deleted.
CREATE INDEX idx_agent_goal_plan_source_run ON agent_goal_plan(source_run_id);
CREATE INDEX idx_agent_goal_plan_approved_review ON agent_goal_plan(approved_review_id);
CREATE INDEX idx_agent_goal_plan_planning_session ON agent_goal_plan(planning_session_id);
