-- Slice 1 (MVP) schema for agent_task.
-- Status describes business lifecycle only; readiness is computed.
-- See plan.md and /Users/vaayne/.agents/sessions/stella/2026-05-28-task-system-v2/plan.md.

CREATE TABLE agent_task (
    id                  TEXT NOT NULL PRIMARY KEY,
    org_id              TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    user_id             TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,  -- D12: creator, NOT assignee
    title               TEXT NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    -- status: Slice 2 widens CHECK to include 'reviewing'.
    status              TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','ready','running','blocked','done','failed','cancelled')),
    priority            TEXT NOT NULL DEFAULT 'routine' CHECK (priority IN ('routine','urgent')),
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

CREATE INDEX idx_agent_task_org_status        ON agent_task(org_id, status);
CREATE INDEX idx_agent_task_status_not_before ON agent_task(status, not_before);
CREATE INDEX idx_agent_task_session           ON agent_task(session_id);
