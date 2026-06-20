-- One execution episode against a goal. Evidence folds in (no evidence
-- table). attempt_no bounds convergence; input_context is frozen at mint; evidence
-- + output are the submitted result; gaps carry the prior evaluation's shortfalls
-- into attempt_no+1. A purpose='decomposition' attempt produces a revision instead
-- of leaf output. Replaces agent_task_run; reuses the goal's session_id.
CREATE TABLE agent_goal_attempt (
    id                  UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    goal_id      TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id            TEXT REFERENCES agent(id) ON DELETE SET NULL,           -- delegator (who minted)
    executor_agent_id   TEXT REFERENCES agent(id) ON DELETE SET NULL,           -- resolved at claim
    session_id          TEXT NOT NULL,                                          -- copied from the goal's persistent session

    purpose             TEXT NOT NULL DEFAULT 'execution',                      -- 'execution'|'decomposition'|'review' (Go-enforced)
    attempt_no          BIGINT NOT NULL DEFAULT 1,                             -- 1-based; unique per (goal, purpose)
    status              TEXT NOT NULL DEFAULT 'queued',                         -- queued|running|submitted|interrupted|failed|cancelled (Go-enforced)

    input_context       JSONB NOT NULL DEFAULT '{}',                            -- frozen at mint: intent + upstream accepted outputs + prior gaps
    evidence            JSONB NOT NULL DEFAULT '{}',                            -- submitted evidence: summary, artifacts-by-hash, stdout refs
    output              JSONB NOT NULL DEFAULT '{}',                            -- candidate output the contract evaluates
    revision_id         UUID REFERENCES agent_goal_revision(id) ON DELETE SET NULL, -- purpose='decomposition': the produced revision
    gaps                JSONB NOT NULL DEFAULT '{}',                            -- evaluation shortfalls (set by acceptance eval; fed to attempt_no+1)
    error               TEXT NOT NULL DEFAULT '',

    -- Lease / heartbeat (carried verbatim from agent_task_run).
    heartbeat_at        TIMESTAMPTZ,
    lease_expires_at    TIMESTAMPTZ,
    worker_id           TEXT NOT NULL DEFAULT '',

    started_at          TIMESTAMPTZ,
    finished_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (attempt_no >= 1)                                                     -- range
);

CREATE INDEX idx_agent_goal_attempt_goal ON agent_goal_attempt(goal_id, attempt_no DESC);
CREATE INDEX idx_agent_goal_attempt_active      ON agent_goal_attempt(status) WHERE status IN ('queued','running');
CREATE INDEX idx_agent_goal_attempt_lease       ON agent_goal_attempt(lease_expires_at) WHERE status IN ('queued','running');
-- <=1 active attempt per (goal, purpose) — the single-writer claim guard.
CREATE UNIQUE INDEX uniq_agent_goal_active_attempt
    ON agent_goal_attempt(goal_id, purpose) WHERE status IN ('queued','running');
-- attempt_no unique within (goal, purpose) — no duplicate iteration.
CREATE UNIQUE INDEX uniq_agent_goal_attempt_no
    ON agent_goal_attempt(goal_id, purpose, attempt_no);

-- Deferred back-edges resolved here (last table in the goal cycle group):
--   agent_goal_revision.source_attempt_id -> agent_goal_attempt
--   agent_goal.active_attempt_id          -> agent_goal_attempt
ALTER TABLE agent_goal_revision
    ADD CONSTRAINT agent_goal_revision_source_attempt_id_fkey
    FOREIGN KEY (source_attempt_id) REFERENCES agent_goal_attempt(id) ON DELETE SET NULL;
ALTER TABLE agent_goal
    ADD CONSTRAINT agent_goal_active_attempt_id_fkey
    FOREIGN KEY (active_attempt_id) REFERENCES agent_goal_attempt(id) ON DELETE SET NULL;
