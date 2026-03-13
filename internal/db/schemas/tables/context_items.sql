CREATE TABLE context_items (
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('message', 'summary')),
    message_id INTEGER REFERENCES messages(id) ON DELETE RESTRICT,
    summary_id TEXT REFERENCES summaries(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, ordinal),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

CREATE INDEX idx_context_items_conv ON context_items(conversation_id, ordinal);
