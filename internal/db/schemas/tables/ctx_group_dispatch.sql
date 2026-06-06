-- ctx_group_dispatch records one durable response attempt per selected agent.
-- The dispatcher may retry this row without re-running the arbiter, which keeps
-- response selection separate from platform delivery failures.
CREATE TABLE ctx_group_dispatch (
    id               TEXT NOT NULL PRIMARY KEY,
    group_message_id TEXT NOT NULL REFERENCES ctx_group_message(id) ON DELETE CASCADE,
    group_id         TEXT NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    agent_id         TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    reply_channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    status           TEXT NOT NULL DEFAULT 'pending',
    attempt_count    INTEGER NOT NULL DEFAULT 0,
    lease_until      TEXT,
    next_attempt_at  TEXT,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (group_message_id, agent_id)
);

CREATE INDEX idx_ctx_group_dispatch_group_id
    ON ctx_group_dispatch(group_id);

CREATE INDEX idx_ctx_group_dispatch_reply_channel
    ON ctx_group_dispatch(reply_channel_id);

CREATE INDEX idx_ctx_group_dispatch_pending
    ON ctx_group_dispatch(status, next_attempt_at, created_at)
    WHERE status = 'pending';

CREATE INDEX idx_ctx_group_dispatch_running_lease
    ON ctx_group_dispatch(status, lease_until)
    WHERE status = 'running';
