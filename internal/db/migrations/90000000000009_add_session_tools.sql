-- +goose Up
-- PostgreSQL 11+ stores a constant column default in the catalog, so this is
-- O(1): legacy rows read as human without a table rewrite or validation scan.
ALTER TABLE ctx_message
    ADD COLUMN actor_type TEXT NOT NULL DEFAULT 'human',
    ADD COLUMN actor_id TEXT,
    ADD COLUMN source_session_id TEXT;

-- Previous-GA rows do not contain per-message evidence of who authored a
-- user-role message. Conversation kind is not author provenance: owners could
-- send human messages to delegate, scheduler, and task conversations through
-- the old Session API. Keep the conservative human default above rather than
-- risk demoting historical principal input.
ALTER TABLE ctx_summary
    ADD COLUMN contains_non_principal_input BOOLEAN NOT NULL DEFAULT false;

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

-- Legacy rows have no inbox link, so the new nullable column and its foreign
-- key can be validated atomically with the rest of this migration.
ALTER TABLE ctx_message
    ADD COLUMN inbox_id UUID,
    ADD CONSTRAINT ctx_message_inbox_id_fkey
        FOREIGN KEY (inbox_id) REFERENCES ctx_session_inbox(id) NOT VALID;
ALTER TABLE ctx_message VALIDATE CONSTRAINT ctx_message_inbox_id_fkey;

-- This PR intentionally ships one atomic migration. Split this index into a
-- NO TRANSACTION migration with CONCURRENTLY if online upgrade writes become
-- a requirement.
CREATE UNIQUE INDEX idx_ctx_message_inbox_id
    ON ctx_message(inbox_id)
    WHERE inbox_id IS NOT NULL;

-- +goose Down
ALTER TABLE ctx_message DROP COLUMN inbox_id;
DROP TABLE ctx_session_inbox;

ALTER TABLE ctx_summary
    DROP COLUMN contains_non_principal_input;

ALTER TABLE ctx_message
    DROP COLUMN source_session_id,
    DROP COLUMN actor_id,
    DROP COLUMN actor_type;
