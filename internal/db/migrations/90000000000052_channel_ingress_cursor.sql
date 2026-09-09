-- +goose Up

-- A cursor is advanced only after the adapter has durably admitted the
-- platform event.  The stream key separates independent platform cursors
-- (for example a Telegram bot from a Discord shard) while the monotonic value
-- makes duplicate acks harmless after a crash.
CREATE TABLE channel_ingress_cursor (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    channel_id TEXT NOT NULL REFERENCES channel(id) ON DELETE CASCADE,
    platform TEXT NOT NULL,
    stream_key TEXT NOT NULL DEFAULT '',
    cursor BIGINT NOT NULL DEFAULT 0,
    -- Discord's gateway session cannot be reconstructed from the sequence
    -- alone. Keep the resume endpoint and lifecycle state beside the cursor so
    -- a process restart either resumes this session or stops visibly.
    session_id TEXT NOT NULL DEFAULT '',
    resume_gateway_url TEXT NOT NULL DEFAULT '',
    session_state TEXT NOT NULL DEFAULT 'new',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT channel_ingress_cursor_identity_present CHECK (channel_id <> '' AND platform <> ''),
    CONSTRAINT channel_ingress_cursor_value_nonnegative CHECK (cursor >= 0),
    CONSTRAINT channel_ingress_cursor_session_state_valid CHECK (session_state IN ('new', 'ready', 'invalid')),
    UNIQUE (channel_id, platform, stream_key)
);

CREATE INDEX idx_channel_ingress_cursor_channel
    ON channel_ingress_cursor(channel_id, platform);

-- +goose Down
DROP INDEX idx_channel_ingress_cursor_channel;
DROP TABLE channel_ingress_cursor;
