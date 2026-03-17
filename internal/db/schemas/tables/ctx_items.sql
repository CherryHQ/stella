CREATE TABLE ctx_items (
    conversation_id INTEGER NOT NULL REFERENCES ctx_conversations(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('message', 'summary')),
    message_id INTEGER REFERENCES ctx_messages(id) ON DELETE RESTRICT,
    summary_id TEXT REFERENCES ctx_summaries(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, ordinal),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

CREATE INDEX idx_ctx_items_conv ON ctx_items(conversation_id, ordinal);
