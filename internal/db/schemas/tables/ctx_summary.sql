CREATE TABLE ctx_summary (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES ctx_conversation(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    depth INTEGER NOT NULL DEFAULT 0,
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    earliest_at TEXT,
    latest_at TEXT,
    descendant_count INTEGER NOT NULL DEFAULT 0,
    descendant_token_count INTEGER NOT NULL DEFAULT 0,
    source_message_token_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_ctx_summary_conv ON ctx_summary(conversation_id, created_at);
