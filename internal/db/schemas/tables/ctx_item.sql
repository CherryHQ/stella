CREATE TABLE ctx_item (
    conversation_id TEXT NOT NULL REFERENCES ctx_conversation(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    item_type TEXT NOT NULL,
    message_id TEXT REFERENCES ctx_message(id) ON DELETE RESTRICT,
    summary_id TEXT REFERENCES ctx_summary(id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, ordinal),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

CREATE INDEX idx_ctx_item_conv ON ctx_item(conversation_id, ordinal);
