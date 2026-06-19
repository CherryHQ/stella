-- The recursive Goal. goal = root (parent_id NULL); children are
-- goals, same shape all the way down. Completion is DERIVED: agents never
-- write acceptance_state — it is a cached projection over agent_goal_acceptance_event,
-- recomputed transactionally by the acceptance evaluator. Child rollup is
-- incremental (required_* counters), never a full-tree scan. session_id is the
-- persistent agent session reused across attempts.
CREATE TABLE agent_goal (
    id                   TEXT NOT NULL PRIMARY KEY,
    user_id              TEXT NOT NULL REFERENCES auth_user(id) ON DELETE CASCADE,
    agent_id             TEXT NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
    project_id           TEXT REFERENCES project(id) ON DELETE SET NULL,

    -- Recursion shape.
    parent_id            TEXT REFERENCES agent_goal(id) ON DELETE CASCADE,  -- NULL = root (goal)
    root_id              TEXT NOT NULL REFERENCES agent_goal(id) ON DELETE CASCADE, -- denormalized tree root; = id for roots
    depth                INTEGER NOT NULL DEFAULT 0,                              -- 0 at root; materializer sets parent.depth+1
    position             INTEGER NOT NULL DEFAULT 0,                              -- sibling order within a parent

    -- Persistent session (carried from agent_task.session_id; reused across attempts).
    session_id           TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,

    -- Intent.
    title                TEXT NOT NULL,
    intent               TEXT NOT NULL DEFAULT '',                                -- the "what & why" prose handed to attempts
    kind                 TEXT NOT NULL DEFAULT 'leaf',                            -- 'leaf'|'composite' (Go-enforced)
    priority             TEXT NOT NULL DEFAULT 'routine',                         -- 'routine'|'urgent' (Go-enforced)
    required             INTEGER NOT NULL DEFAULT 1,                              -- 0/1: does parent acceptance depend on this child?

    -- Contract & policy (JSON; schemas enforced in Go).
    acceptance_contract  TEXT NOT NULL DEFAULT '{}',                             -- composite policy tree of deterministic+judgment items
    convergence_policy   TEXT NOT NULL DEFAULT '{}',                             -- {max_attempts, escalation, max_depth}
    review_policy        TEXT NOT NULL DEFAULT 'none',                           -- decomposition gate: 'none' auto-accepts; 'human' awaits (Go-enforced)

    -- Lifecycle (single-writer owned; Go-enforced value set).
    lifecycle            TEXT NOT NULL DEFAULT 'draft',                          -- draft|ready|active|blocked|accepted|rejected_final|abandoned|cancelled
    block_reason         TEXT NOT NULL DEFAULT '',                               -- budget_exhausted|needs_verdict|dep|'' (meaningful only when lifecycle='blocked')

    -- Derived acceptance projection (CACHE over acceptance_event; never freely mutated).
    acceptance_state     TEXT NOT NULL DEFAULT 'pending',                        -- pending|passed|failed (last evaluation result)
    accepted_output      TEXT,                                                   -- frozen accepted output snapshot; NULL until accepted
    acceptance_seq       INTEGER NOT NULL DEFAULT 0,                             -- # of acceptance_events folded; fences stale projections

    -- Active attempt pointer + convergence counter.
    active_attempt_id    TEXT REFERENCES agent_goal_attempt(id) ON DELETE SET NULL,
    attempt_count        INTEGER NOT NULL DEFAULT 0,                             -- attempts minted so far; bounds convergence (replaces retry_count)

    -- Incremental child rollup counters (composite only). Bumped in the SAME tx that
    -- transitions a child, so a parent never scans its subtree per event.
    required_total       INTEGER NOT NULL DEFAULT 0,                             -- count of required children materialized
    required_accepted    INTEGER NOT NULL DEFAULT 0,                             -- required children accepted
    required_failed      INTEGER NOT NULL DEFAULT 0,                             -- required children rejected_final/abandoned/cancelled
    required_blocked     INTEGER NOT NULL DEFAULT 0,                             -- required children blocked (advisory; surfaces stalls)

    -- Decomposition pointer (composite): the accepted+materialized revision.
    accepted_revision_id TEXT REFERENCES agent_goal_revision(id) ON DELETE SET NULL,

    -- Context blobs.
    context              TEXT NOT NULL DEFAULT '{}',                             -- progress patches / freeform metadata (ex-agent_task.context)
    dispatch_hint        TEXT NOT NULL DEFAULT '{}',                             -- {executor_agent_id, consumed_at} — folds agent_task_dispatch_hint into a column

    created_at           TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT NOT NULL DEFAULT (datetime('now')),
    accepted_at          TEXT,
    cancelled_at         TEXT,
    archived_at          TEXT,

    -- Structural invariants only (schema-design.md: no enum-value CHECKs).
    CHECK (parent_id IS NULL OR parent_id != id),                               -- no self-parent (self-ref)
    CHECK (parent_id IS NOT NULL OR root_id = id),                              -- a root is its own root (coupling)
    CHECK (required IN (0,1)),                                                  -- range
    CHECK (depth >= 0 AND attempt_count >= 0),                                  -- range
    CHECK (required_total >= 0 AND required_accepted >= 0
           AND required_failed >= 0 AND required_blocked >= 0),                -- range
    -- Anti-drift: lifecycle='accepted' iff the projection passed AND output frozen
    -- (spec invariant #2 — pushed into the DB instead of trusting the single writer).
    CHECK (lifecycle != 'accepted'
           OR (acceptance_state = 'passed' AND accepted_output IS NOT NULL))
);

-- FK + hot-path indexes.
CREATE INDEX idx_agent_goal_parent            ON agent_goal(parent_id, position);
CREATE INDEX idx_agent_goal_root              ON agent_goal(root_id);
CREATE INDEX idx_agent_goal_user_created      ON agent_goal(user_id, created_at DESC, id DESC);
CREATE INDEX idx_agent_goal_user_archived_created ON agent_goal(user_id, archived_at, created_at DESC, id DESC);
CREATE INDEX idx_agent_goal_agent_project     ON agent_goal(agent_id, project_id);
CREATE INDEX idx_agent_goal_project           ON agent_goal(project_id);
CREATE INDEX idx_agent_goal_active_attempt    ON agent_goal(active_attempt_id);
CREATE INDEX idx_agent_goal_accepted_revision ON agent_goal(accepted_revision_id);
CREATE UNIQUE INDEX uniq_agent_goal_session   ON agent_goal(session_id);
-- Dispatcher candidate scan: a leaf, ready, no in-flight attempt, hottest first.
CREATE INDEX idx_agent_goal_dispatchable
    ON agent_goal(priority DESC, created_at ASC)
    WHERE lifecycle = 'ready' AND active_attempt_id IS NULL AND kind = 'leaf';
-- Rollup scan: composite parents possibly past their counter threshold.
CREATE INDEX idx_agent_goal_rollup_candidates
    ON agent_goal(root_id, updated_at ASC)
    WHERE kind = 'composite' AND lifecycle = 'active';
