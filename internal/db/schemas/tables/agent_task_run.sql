-- One execution attempt against a task.
-- Slice 1: kind = 'worker' only. Slice 2 widens to 'reviewer'. Slice 3 widens to
-- 'planner','synthesizer' and adds goal_id with XOR + kind-coupling CHECK.
--
-- session_id is always populated (D12  every run records the session it ran in).
-- executor_agent_id is resolved at claim time per D13.

CREATE TABLE agent_task_run (
    id                  TEXT NOT NULL PRIMARY KEY,
    task_id             TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    org_id              TEXT NOT NULL REFERENCES auth_organization(id) ON DELETE CASCADE,
    user_id             TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,   -- delegator (creator of this run)
    executor_agent_id   TEXT REFERENCES settings_agent(id) ON DELETE SET NULL,   -- D13: resolved at claim
    kind                TEXT NOT NULL DEFAULT 'worker' CHECK (kind IN ('worker','reviewer')),
    attempt_no          INTEGER NOT NULL DEFAULT 1,
    status              TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','completed','failed','cancelled','interrupted','timed_out')),
    session_id          TEXT NOT NULL,
    input               TEXT NOT NULL DEFAULT '{}',
    result              TEXT NOT NULL DEFAULT '{}',
    error               TEXT NOT NULL DEFAULT '',
    heartbeat_at        TEXT,                                                     -- B6: worker liveness
    lease_expires_at    TEXT,                                                     -- B6: stale-run detection
    worker_id           TEXT NOT NULL DEFAULT '',                                 -- B6: optional process tag
    started_at          TEXT,
    finished_at         TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
    -- Slice 1: every run must have a task_id (no goal_id column yet).
    -- Slice 3 will swap this for the XOR + kind-coupling CHECK.
    CHECK (task_id IS NOT NULL)
);

CREATE INDEX idx_agent_task_run_task    ON agent_task_run(task_id, attempt_no DESC);
CREATE INDEX idx_agent_task_run_active  ON agent_task_run(status) WHERE status IN ('queued','running');
CREATE INDEX idx_agent_task_run_lease   ON agent_task_run(lease_expires_at) WHERE status IN ('queued','running');

-- Invariant: 1 active worker run per task (HP2).
CREATE UNIQUE INDEX uniq_active_worker_run
    ON agent_task_run(task_id) WHERE task_id IS NOT NULL AND kind = 'worker' AND status IN ('queued','running');

-- Slice 2: 1 active reviewer run per task (HP2).
CREATE UNIQUE INDEX uniq_active_reviewer_run
    ON agent_task_run(task_id) WHERE task_id IS NOT NULL AND kind = 'reviewer' AND status IN ('queued','running');

-- Invariant: attempt_no unique within (task_id, kind) (HP3).
CREATE UNIQUE INDEX uniq_task_run_attempt
    ON agent_task_run(task_id, kind, attempt_no) WHERE task_id IS NOT NULL;
