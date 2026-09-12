-- +goose Up
-- Durable session event log: every live turn event is also appended here with
-- a session-scoped sequence, so a watcher on any replica can replay or tail a
-- turn the local SessionHub never saw. The in-memory hub stays as a fast path;
-- this log is the cross-process truth and the reconnect cursor.
CREATE TABLE ctx_session_event (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id TEXT        NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE CASCADE,
    run_id     UUID        REFERENCES agent_run(id) ON DELETE SET NULL,
    -- Per-session contiguous sequence; assigned under the session advisory lock.
    seq        BIGINT      NOT NULL,
    -- JSON of the serializable event subset (text/reasoning/tool_use/step/
    -- references/image/file/error). Transport-internal fields (Store) are not
    -- stored — history rows already carry them.
    event      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Replay/tail scan: WHERE session_id=$ AND seq>$ ORDER BY seq.
ALTER TABLE ctx_session_event
    ADD CONSTRAINT ctx_session_event_session_seq UNIQUE (session_id, seq);
CREATE INDEX idx_ctx_session_event_session_seq ON ctx_session_event (session_id, seq);
-- Retention sweep by age.
CREATE INDEX idx_ctx_session_event_created ON ctx_session_event (created_at);

-- +goose Down
DROP TABLE ctx_session_event;
