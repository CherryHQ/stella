-- +goose Up
SET LOCAL lock_timeout = '10s';

-- One boot identity is created for each stellad process. It is deliberately
-- separate from AgentRun so a recovered process can never inherit ownership.
CREATE TABLE runtime_executor_boot (
    id UUID PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'starting',
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    drained_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runtime_executor_boot_status_valid CHECK (status IN ('starting', 'running', 'drained')),
    CONSTRAINT runtime_executor_boot_drain_shape CHECK (
        (status = 'drained' AND drained_at IS NOT NULL)
        OR (status <> 'drained' AND drained_at IS NULL)
    )
);

-- AgentRun is the one durable execution owner for a Session. The partial
-- unique index is the cross-process admission fence; process-local busy maps
-- remain only a fast-path optimization.
--
-- Run terminal state is execution-only: a Run ends when its turn's output and
-- the source-domain bookkeeping that owns it are committed. External delivery
-- is a channel concern and gets no column here, so a Run can never stay alive
-- waiting for a platform to acknowledge bytes.
CREATE TABLE agent_run (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    executor_boot_id UUID NOT NULL REFERENCES runtime_executor_boot(id) ON DELETE RESTRICT,
    source TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    lease_expires_at TIMESTAMPTZ NOT NULL,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    abort_requested_at TIMESTAMPTZ,
    abort_reason TEXT NOT NULL DEFAULT '',
    terminal_reason TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_run_status_valid CHECK (
        status IN ('running', 'completed', 'failed', 'canceled', 'aborted', 'interrupted')
    ),
    CONSTRAINT agent_run_source_present CHECK (source <> ''),
    CONSTRAINT agent_run_terminal_shape CHECK (
        (status = 'running' AND completed_at IS NULL)
        OR (status <> 'running' AND completed_at IS NOT NULL)
    ),
    CONSTRAINT agent_run_abort_shape CHECK (
        (abort_requested_at IS NULL AND abort_reason = '')
        OR abort_requested_at IS NOT NULL
    )
);

CREATE UNIQUE INDEX idx_agent_run_one_running_session
    ON agent_run(session_id)
    WHERE status = 'running';
CREATE INDEX idx_agent_run_running_lease
    ON agent_run(lease_expires_at)
    WHERE status = 'running';
CREATE INDEX idx_agent_run_executor
    ON agent_run(executor_boot_id, status);

-- A Session inbox receipt is linked in the same transaction that creates its
-- AgentRun. A linked row follows that Run's terminal state during recovery and
-- is never replayed as a fresh model turn.
ALTER TABLE ctx_session_inbox
    ADD COLUMN run_id UUID REFERENCES agent_run(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX idx_ctx_session_inbox_run_id
    ON ctx_session_inbox(run_id)
    WHERE run_id IS NOT NULL;

-- One delivery record for a Run whose output has an external delivery target (a
-- channel chat). It is the handoff that survives execution ownership: it names
-- the channel instance that owns the reply, the routing the reply needs, the
-- committed text and media it carries, and what became of the delivery attempt.
--
-- States describe the model output, not the channel's own operational messages:
--   live   the Run holds execution; streamed deltas are fenced by that lease and
--          no committed output exists yet
--   ready  the turn committed its output together with the Run's terminal
--          transition; the owning channel may deliver it
--   sent | not_sent | unknown   the one delivery attempt is over
--
-- `attempt_at` is written before any external request, so a row with
-- `attempt_at IS NULL` is the only thing a later delivery owner may ever act on.
-- This is deliberately not a queue and not a second Run state axis: there is no
-- lease, no worker, and no retry policy here, and a Run is terminal regardless of
-- this row's state.
CREATE TABLE agent_run_output (
    run_id UUID PRIMARY KEY REFERENCES agent_run(id) ON DELETE RESTRICT,
    session_id TEXT NOT NULL,
    platform TEXT NOT NULL DEFAULT '',
    channel_id TEXT NOT NULL DEFAULT '',
    chat_id TEXT NOT NULL DEFAULT '',
    thread_id TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    media JSONB NOT NULL DEFAULT '[]',
    state TEXT NOT NULL DEFAULT 'live',
    attempt_at TIMESTAMPTZ,
    settled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_run_output_state_valid CHECK (
        state IN ('live', 'ready', 'sent', 'not_sent', 'unknown')
    ),
    CONSTRAINT agent_run_output_state_shape CHECK (
        (state IN ('live', 'ready') AND settled_at IS NULL)
        OR (state IN ('sent', 'not_sent', 'unknown') AND settled_at IS NOT NULL)
    )
);

-- The only rows a delivery owner without a live attempt may pick up.
CREATE INDEX idx_agent_run_output_ready_unattempted
    ON agent_run_output(created_at)
    WHERE state = 'ready' AND attempt_at IS NULL;

-- Rich reply bytes travel with the output, never as executor-local file paths.
-- Chunks bound memory independently of file size. A reply becomes ready only
-- after every chunk is committed; partial captures remain non-deliverable.
CREATE TABLE agent_run_output_part (
    run_id UUID NOT NULL REFERENCES agent_run_output(run_id) ON DELETE CASCADE,
    event_no BIGINT NOT NULL,
    chunk_no INTEGER NOT NULL,
    metadata JSONB NOT NULL,
    data BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, event_no, chunk_no),
    CONSTRAINT agent_run_output_part_position_valid CHECK (event_no > 0 AND chunk_no >= 0),
    CONSTRAINT agent_run_output_part_size_valid CHECK (octet_length(data) <= 1048576)
);

-- +goose Down
DROP TABLE agent_run_output_part;
DROP INDEX idx_agent_run_output_ready_unattempted;
DROP TABLE agent_run_output;
DROP INDEX idx_ctx_session_inbox_run_id;
ALTER TABLE ctx_session_inbox DROP COLUMN run_id;
DROP TABLE agent_run;
DROP TABLE runtime_executor_boot;
