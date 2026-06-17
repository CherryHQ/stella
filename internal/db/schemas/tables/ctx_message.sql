CREATE TABLE ctx_message (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES ctx_conversation(id) ON DELETE CASCADE,
    seq BIGINT NOT NULL,
    role TEXT NOT NULL,
    event_type TEXT NOT NULL DEFAULT 'text',
    content TEXT NOT NULL,
    token_count BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    content_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    UNIQUE (conversation_id, seq)
);

CREATE INDEX idx_ctx_message_conv_seq ON ctx_message(conversation_id, seq);
CREATE INDEX idx_ctx_message_tsv ON ctx_message USING GIN (content_tsv);
CREATE INDEX idx_ctx_message_content_trgm ON ctx_message USING GIN (content gin_trgm_ops);
