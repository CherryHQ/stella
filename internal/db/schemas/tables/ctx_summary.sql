CREATE TABLE ctx_summary (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES ctx_conversation(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    depth BIGINT NOT NULL DEFAULT 0,
    content TEXT NOT NULL,
    token_count BIGINT NOT NULL,
    earliest_at TIMESTAMPTZ,
    latest_at TIMESTAMPTZ,
    descendant_count BIGINT NOT NULL DEFAULT 0,
    descendant_token_count BIGINT NOT NULL DEFAULT 0,
    source_message_token_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    content_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED
);

CREATE INDEX idx_ctx_summary_conv ON ctx_summary(conversation_id, created_at);
CREATE INDEX idx_ctx_summary_tsv ON ctx_summary USING GIN (content_tsv);
CREATE INDEX idx_ctx_summary_content_trgm ON ctx_summary USING GIN (content gin_trgm_ops);
