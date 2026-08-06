-- +goose Up
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
    ADD COLUMN guest_id UUID REFERENCES channel_guest(id) ON DELETE CASCADE;

CREATE INDEX idx_ctx_conversation_guest_id ON ctx_conversation(guest_id);

ALTER TABLE ctx_conversation
    ADD CONSTRAINT ctx_conversation_guest_scope_check CHECK (
        guest_id IS NULL OR (
            kind = 'chat'
            AND user_id = guest_id::text
            AND group_id IS NULL
            AND project_id IS NULL
        )
    );

CREATE UNIQUE INDEX idx_one_agent_guest_chat
    ON ctx_conversation(agent_id, guest_id)
    WHERE kind = 'chat' AND archived = false AND guest_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_one_agent_guest_chat;
ALTER TABLE ctx_conversation DROP CONSTRAINT ctx_conversation_guest_scope_check;
DROP INDEX idx_ctx_conversation_guest_id;
ALTER TABLE ctx_conversation DROP COLUMN guest_id;
DROP TABLE channel_guest;
