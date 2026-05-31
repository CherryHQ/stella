-- One execution attempt against a task.
-- Slice 1: kind = 'worker' only. Slice 2 widens to 'reviewer'. Slice 3 widens to
-- 'planner','synthesizer' and adds goal_id with XOR + kind-coupling CHECK.
--
-- session_id is always populated (D12  every run records the session it ran in).
-- executor_agent_id is resolved at claim time per D13.

CREATE TABLE agent_task_run (
    id                  TEXT NOT NULL PRIMARY KEY,
    task_id             TEXT REFERENCES agent_task(id) ON DELETE CASCADE,
    goal_id             TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,
    user_id             TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT REFERENCES agent(id) ON DELETE SET NULL,   -- delegator (creator of this run)
    executor_agent_id   TEXT REFERENCES agent(id) ON DELETE SET NULL,   -- D13: resolved at claim
    kind                TEXT NOT NULL DEFAULT 'worker' CHECK (kind IN ('worker','reviewer','planner','synthesizer')),
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
    -- Slice 3: XOR parent, kind ↔ parent type coupling (B3).
    CHECK (
      (task_id IS NOT NULL AND goal_id IS NULL     AND kind IN ('worker','reviewer'))
      OR
      (task_id IS NULL     AND goal_id IS NOT NULL AND kind IN ('planner','synthesizer'))
    )
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

-- Slice 3: 1 active planner / synthesizer run per goal (HP2).
CREATE UNIQUE INDEX uniq_active_planner_run
    ON agent_task_run(goal_id) WHERE goal_id IS NOT NULL AND kind = 'planner' AND status IN ('queued','running');
CREATE UNIQUE INDEX uniq_active_synthesizer_run
    ON agent_task_run(goal_id) WHERE goal_id IS NOT NULL AND kind = 'synthesizer' AND status IN ('queued','running');

CREATE INDEX idx_agent_task_run_goal ON agent_task_run(goal_id);
CREATE UNIQUE INDEX uniq_goal_run_attempt
    ON agent_task_run(goal_id, kind, attempt_no) WHERE goal_id IS NOT NULL;

-- Invariant: attempt_no unique within (task_id, kind) (HP3).
CREATE UNIQUE INDEX uniq_task_run_attempt
    ON agent_task_run(task_id, kind, attempt_no) WHERE task_id IS NOT NULL;
