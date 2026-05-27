CREATE TABLE ctx_message_part (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES ctx_message(id) ON DELETE CASCADE,
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
