-- A decomposition version of a composite deliverable (the former agent_goal_plan).
-- Produced as the output of a purpose='decomposition' attempt; content is the
-- proposed child deliverables + edges. Gated by the parent's review_policy:
-- 'none' auto-accepts, 'human' awaits approval. Accepted → materialize children +
-- edges in ONE tx. Versioned by revision_no so a replan is a new revision, not an
-- in-place edit. materialized_at is a FACT about an accepted revision, not a status.
CREATE TABLE agent_dlv_revision (
    id                  TEXT NOT NULL PRIMARY KEY,
    deliverable_id      TEXT NOT NULL REFERENCES agent_dlv_deliverable(id) ON DELETE CASCADE, -- the composite being decomposed
    revision_no         INTEGER NOT NULL DEFAULT 1,                            -- monotonic per deliverable; replan = revision_no+1
    status              TEXT NOT NULL DEFAULT 'draft',                         -- draft|in_review|accepted|rejected|superseded (Go-enforced)
    review_policy       TEXT NOT NULL DEFAULT 'none',                          -- snapshot of the gate at submit (Go-enforced)

    content             TEXT NOT NULL DEFAULT '{}',                            -- proposed children + edges (DecompositionContent)
    source_attempt_id   TEXT REFERENCES agent_dlv_attempt(id) ON DELETE SET NULL, -- the decomposition attempt that produced it

    -- Dedicated planning session (carried from agent_goal_plan.planning_session_id).
    planning_session_id TEXT REFERENCES ctx_conversation(session_id) ON DELETE SET NULL,

    accepted_at         TEXT,
    materialized_at     TEXT,                                                  -- stamped when children materialized (idempotency fence)
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),

    CHECK (revision_no >= 1),                                                  -- range
    -- Monotonic gate: a revision can only be materialized after it is accepted.
    CHECK (materialized_at IS NULL OR accepted_at IS NOT NULL)                 -- coupling
);

CREATE INDEX idx_agent_dlv_revision_deliverable      ON agent_dlv_revision(deliverable_id, revision_no DESC);
CREATE INDEX idx_agent_dlv_revision_planning_session ON agent_dlv_revision(planning_session_id);
CREATE INDEX idx_agent_dlv_revision_source_attempt   ON agent_dlv_revision(source_attempt_id);
CREATE UNIQUE INDEX uniq_agent_dlv_revision_no       ON agent_dlv_revision(deliverable_id, revision_no);
-- One OPEN (draft/in_review) revision per deliverable.
CREATE UNIQUE INDEX uniq_agent_dlv_open_revision
    ON agent_dlv_revision(deliverable_id) WHERE status IN ('draft','in_review');
-- One MATERIALIZED (live decomposition) revision per deliverable — complements the
-- open-side singleton so both ends are DB facts.
CREATE UNIQUE INDEX uniq_agent_dlv_materialized_revision
    ON agent_dlv_revision(deliverable_id) WHERE materialized_at IS NOT NULL;
