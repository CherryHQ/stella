-- A decomposition version of a composite goal (the former agent_goal_plan).
-- Produced as the output of a purpose='decomposition' attempt; content is the
-- proposed child goals + edges. Gated by the parent's review_policy:
-- 'none' auto-accepts, 'human' awaits approval. Accepted → materialize children +
-- edges in ONE tx. Versioned by revision_no so a replan is a new revision, not an
-- in-place edit. materialized_at is a FACT about an accepted revision, not a status.
CREATE TABLE agent_goal_revision (
    id                  TEXT NOT NULL PRIMARY KEY,
    goal_id      TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE, -- the composite being decomposed
    revision_no         BIGINT NOT NULL DEFAULT 1,                            -- monotonic per goal; replan = revision_no+1
    status              TEXT NOT NULL DEFAULT 'draft',                         -- draft|in_review|accepted|rejected|superseded (Go-enforced)
    review_policy       TEXT NOT NULL DEFAULT 'none',                          -- snapshot of the gate at submit (Go-enforced)

    content             JSONB NOT NULL DEFAULT '{}',                            -- proposed children + edges (DecompositionContent)
    source_attempt_id   TEXT,  -- FK added in agent_goal_attempt.sql (cycle: agent_goal_revision <-> agent_goal_attempt)

    -- Dedicated planning session (carried from agent_goal_plan.planning_session_id).
    planning_session_id TEXT REFERENCES ctx_conversation(session_id) ON DELETE SET NULL,

    accepted_at         TIMESTAMPTZ,
    materialized_at     TIMESTAMPTZ,                                                  -- stamped when children materialized (idempotency fence)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    CHECK (revision_no >= 1),                                                  -- range
    -- Monotonic gate: a revision can only be materialized after it is accepted.
    CHECK (materialized_at IS NULL OR accepted_at IS NOT NULL)                 -- coupling
);

CREATE INDEX idx_agent_goal_revision_goal      ON agent_goal_revision(goal_id, revision_no DESC);
CREATE INDEX idx_agent_goal_revision_planning_session ON agent_goal_revision(planning_session_id);
CREATE INDEX idx_agent_goal_revision_source_attempt   ON agent_goal_revision(source_attempt_id);
CREATE UNIQUE INDEX uniq_agent_goal_revision_no       ON agent_goal_revision(goal_id, revision_no);
-- One OPEN (draft/in_review) revision per goal.
CREATE UNIQUE INDEX uniq_agent_goal_open_revision
    ON agent_goal_revision(goal_id) WHERE status IN ('draft','in_review');
-- One MATERIALIZED (live decomposition) revision per goal — complements the
-- open-side singleton so both ends are DB facts.
CREATE UNIQUE INDEX uniq_agent_goal_materialized_revision
    ON agent_goal_revision(goal_id) WHERE materialized_at IS NOT NULL;

-- Deferred back-edge: agent_goal.accepted_revision_id -> agent_goal_revision.
-- (cycle agent_goal <-> agent_goal_revision; broken here per main.sql topo order)
ALTER TABLE agent_goal
    ADD CONSTRAINT agent_goal_accepted_revision_id_fkey
    FOREIGN KEY (accepted_revision_id) REFERENCES agent_goal_revision(id) ON DELETE SET NULL;
