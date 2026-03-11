CREATE TABLE conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    bootstrapped_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (conversation_id, seq)
);

CREATE TABLE message_parts (
    id TEXT PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    part_type TEXT NOT NULL CHECK (part_type IN ('text', 'reasoning', 'tool')),
    ordinal INTEGER NOT NULL,
    text_content TEXT,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_output TEXT,
    metadata TEXT,
    UNIQUE (message_id, ordinal)
);

CREATE TABLE summaries (
    id TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('leaf', 'condensed')),
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

CREATE TABLE summary_messages (
    summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);

CREATE TABLE summary_parents (
    summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    parent_summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, parent_summary_id)
);

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

-- Indexes
CREATE INDEX idx_messages_conv_seq ON messages(conversation_id, seq);
CREATE INDEX idx_summaries_conv ON summaries(conversation_id, created_at);
CREATE INDEX idx_context_items_conv ON context_items(conversation_id, ordinal);
