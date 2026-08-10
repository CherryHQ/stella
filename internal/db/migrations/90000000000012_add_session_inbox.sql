-- +goose Up
CREATE TABLE ctx_session_inbox (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    enqueue_seq BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
    source_session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    target_session_id TEXT NOT NULL REFERENCES ctx_conversation(session_id) ON DELETE RESTRICT,
    actor_id TEXT NOT NULL,
    content TEXT NOT NULL,
    delivered_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ,
    error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ctx_session_inbox_distinct_sessions CHECK (source_session_id <> target_session_id),
    CONSTRAINT ctx_session_inbox_single_terminal_state CHECK (NOT (delivered_at IS NOT NULL AND failed_at IS NOT NULL)),
    CONSTRAINT ctx_session_inbox_failure_code CHECK ((failed_at IS NULL) = (error_code = ''))
);

CREATE INDEX idx_ctx_session_inbox_pending
    ON ctx_session_inbox(target_session_id, enqueue_seq)
    WHERE delivered_at IS NULL AND failed_at IS NULL;

-- Keep the populated message-table change metadata-only, then validate the
-- all-NULL legacy rows without taking an ACCESS EXCLUSIVE lock for the scan.
ALTER TABLE ctx_message
    ADD COLUMN inbox_id UUID,
    ADD CONSTRAINT ctx_message_inbox_id_fkey
        FOREIGN KEY (inbox_id) REFERENCES ctx_session_inbox(id) NOT VALID;
ALTER TABLE ctx_message VALIDATE CONSTRAINT ctx_message_inbox_id_fkey;

-- +goose Down
ALTER TABLE ctx_message DROP COLUMN inbox_id;
DROP TABLE ctx_session_inbox;
