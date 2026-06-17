-- ctx_group_outbox is the durable work queue for human group messages that
-- need arbiter materialization. It is written in the same transaction as the
-- deduplicated ctx_group_message row so ingest cannot commit without dispatch
-- work becoming recoverable.
CREATE TABLE ctx_group_outbox (
    id               TEXT NOT NULL PRIMARY KEY,
    group_message_id TEXT NOT NULL UNIQUE REFERENCES ctx_group_message(id) ON DELETE CASCADE,
    group_id         TEXT NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    envelope         TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'pending',
    attempt_count    BIGINT NOT NULL DEFAULT 0,
    lease_until      TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ctx_group_outbox_group_id
    ON ctx_group_outbox(group_id);

CREATE INDEX idx_ctx_group_outbox_pending
    ON ctx_group_outbox(status, next_attempt_at, created_at)
    WHERE status = 'pending';

CREATE INDEX idx_ctx_group_outbox_running_lease
    ON ctx_group_outbox(status, lease_until)
    WHERE status = 'running';
