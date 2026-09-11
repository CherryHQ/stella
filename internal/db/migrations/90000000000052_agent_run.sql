-- +goose Up
-- Durable unit of accepted agent work. A run exists once input is committed;
-- workers claim it under the session execution lease. State transitions are
-- one-way toward terminal states (completed/failed/canceled/interrupted) and
-- enforced in the store layer.
CREATE TABLE agent_run (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    -- Ingress event that produced this run. NULL for non-channel input (Web).
    -- SET NULL so audit retention can eventually detach without breaking
    -- session deletion.
    inbox_id       UUID REFERENCES channel_inbox(id) ON DELETE SET NULL,
    session_id     TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    agent_id       TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    -- End-to-end idempotency key, namespaced by the caller's source
    -- (e.g. "inbox:<uuid>" for channel input). One logical request mints at
    -- most one run; retries attach, never duplicate.
    request_key    TEXT NOT NULL,
    UNIQUE (request_key),
    -- enqueue_seq is allocated under the session row lock; this constraint is
    -- the backstop that turns a missed lock into an error, not a wrong order.
    UNIQUE (session_id, enqueue_seq),
    -- Versioned value objects: who asked, what was asked, where the final
    -- answer goes. Snapshots, so later config changes cannot rewrite history.
    actor          JSONB NOT NULL DEFAULT '{}',
    input          JSONB NOT NULL DEFAULT '{}',
    reply_address  JSONB NOT NULL DEFAULT '{}',
    -- Queue order within the session, assigned under the session row lock so
    -- a later request can never overtake an earlier non-terminal run.
    enqueue_seq    BIGINT NOT NULL CHECK (enqueue_seq >= 0),
    -- queued / running / completed / failed / canceled / interrupted. In Go.
    state          TEXT NOT NULL DEFAULT 'queued',
    -- Worker replica currently executing, for observability only; authority
    -- comes from ctx_session_execution.
    worker_id      TEXT,
    error_code     TEXT,
    -- Predecessor when this run is a bounded recovery retry.
    retry_of_run_id UUID REFERENCES agent_run(id) ON DELETE SET NULL,
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (retry_of_run_id IS NULL OR retry_of_run_id != id)
);

-- Worker scan: oldest queued runs first, FOR UPDATE SKIP LOCKED.
CREATE INDEX idx_agent_run_queued ON agent_run (enqueue_seq)
    WHERE state = 'queued';
-- Per-session non-terminal ordering.
CREATE INDEX idx_agent_run_session_open ON agent_run (session_id, enqueue_seq)
    WHERE state IN ('queued', 'running');
-- At most one running run per session.
CREATE UNIQUE INDEX idx_agent_run_one_running_per_session ON agent_run (session_id)
    WHERE state = 'running';
CREATE INDEX idx_agent_run_inbox ON agent_run (inbox_id) WHERE inbox_id IS NOT NULL;
CREATE INDEX idx_agent_run_retry_of ON agent_run (retry_of_run_id) WHERE retry_of_run_id IS NOT NULL;

-- The execution lease is the single-writer fence for a session; linking it to
-- its run lets a reaper and the completion transaction agree on which run the
-- token covers. SET NULL keeps the lease usable if its run row is removed.
ALTER TABLE ctx_session_execution
    ADD COLUMN run_id UUID REFERENCES agent_run(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE ctx_session_execution DROP COLUMN run_id;
DROP TABLE agent_run;
