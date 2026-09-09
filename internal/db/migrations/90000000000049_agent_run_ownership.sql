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
CREATE TABLE agent_run (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    executor_boot_id UUID NOT NULL REFERENCES runtime_executor_boot(id) ON DELETE RESTRICT,
    source TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    completion_state TEXT NOT NULL DEFAULT 'open',
    completion_outcome TEXT NOT NULL DEFAULT '',
    completion_status TEXT NOT NULL DEFAULT 'completed',
    completion_reason TEXT NOT NULL DEFAULT '',
    completion_ready_at TIMESTAMPTZ,
    completion_acked_at TIMESTAMPTZ,
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
    CONSTRAINT agent_run_completion_state_valid CHECK (
        completion_state IN ('open', 'ready', 'acked', 'unknown')
    ),
    CONSTRAINT agent_run_completion_outcome_valid CHECK (
        completion_outcome IN ('', 'delivered', 'failed', 'discarded', 'unknown')
    ),
    CONSTRAINT agent_run_completion_shape CHECK (
        (status = 'running' AND completion_state = 'open' AND completion_outcome = '' AND completion_ready_at IS NULL AND completion_acked_at IS NULL)
        OR (status = 'running' AND completion_state = 'ready' AND completion_outcome = '' AND completion_ready_at IS NOT NULL AND completion_acked_at IS NULL)
        OR (status <> 'running' AND completion_state = 'acked' AND completion_outcome <> '' AND completion_ready_at IS NOT NULL AND completion_acked_at IS NOT NULL)
        OR (status <> 'running' AND completion_state = 'unknown' AND completion_outcome = 'unknown' AND completion_ready_at IS NOT NULL AND completion_acked_at IS NULL)
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

-- +goose Down
DROP INDEX idx_ctx_session_inbox_run_id;
ALTER TABLE ctx_session_inbox DROP COLUMN run_id;
DROP TABLE agent_run;
DROP TABLE runtime_executor_boot;
