CREATE TABLE ctx_message (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES ctx_conversation(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    event_type TEXT NOT NULL DEFAULT 'text',
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (conversation_id, seq)
);

CREATE INDEX idx_ctx_message_conv_seq ON ctx_message(conversation_id, seq);
