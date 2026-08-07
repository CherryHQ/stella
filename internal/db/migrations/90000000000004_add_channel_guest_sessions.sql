-- +goose Up
-- Bound every DDL lock acquisition: startup should fail and retry rather than
-- queue indefinitely behind live conversation traffic.
SET LOCAL lock_timeout = '10s';

CREATE TABLE channel_guest (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    platform TEXT NOT NULL,
    external_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (channel_id, platform, external_id)
);

CREATE INDEX idx_channel_guest_channel_id ON channel_guest(channel_id);

ALTER TABLE ctx_conversation
    ADD COLUMN guest_id UUID;

ALTER TABLE ctx_conversation
    ADD CONSTRAINT ctx_conversation_guest_scope_check CHECK (
        guest_id IS NULL OR (
            kind = 'chat'
            AND user_id = guest_id::text
            AND group_id IS NULL
            AND project_id IS NULL
        )
    ) NOT VALID,
    ADD CONSTRAINT ctx_conversation_guest_id_fkey
        FOREIGN KEY (guest_id) REFERENCES channel_guest(id) ON DELETE CASCADE
        NOT VALID;

-- VALIDATE CONSTRAINT normally takes only SHARE UPDATE EXCLUSIVE, but goose
-- wraps this migration in one transaction, so the ACCESS EXCLUSIVE lock taken
-- by ADD CONSTRAINT above is still held here: the split buys no concurrency on
-- its own. It is kept because the scan it bounds is what would otherwise make
-- that lock long-lived, and because the NOT VALID form is what a future
-- deployment needs if guest_id is ever backfilled. The new guest_id column is
-- NULL for every pre-migration row, so both validations scan a table with no
-- candidate rows and finish immediately.
ALTER TABLE ctx_conversation
    VALIDATE CONSTRAINT ctx_conversation_guest_scope_check;

ALTER TABLE ctx_conversation
    VALIDATE CONSTRAINT ctx_conversation_guest_id_fkey;

-- +goose Down
ALTER TABLE ctx_conversation DROP CONSTRAINT ctx_conversation_guest_id_fkey;
ALTER TABLE ctx_conversation DROP CONSTRAINT ctx_conversation_guest_scope_check;
ALTER TABLE ctx_conversation DROP COLUMN guest_id;
DROP TABLE channel_guest;
