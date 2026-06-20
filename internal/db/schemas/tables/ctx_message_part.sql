CREATE TABLE ctx_message_part (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    message_id UUID NOT NULL REFERENCES ctx_message(id) ON DELETE CASCADE,
    part_type TEXT NOT NULL,
    ordinal BIGINT NOT NULL,
    text_content TEXT,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_output TEXT,
    metadata TEXT,
    UNIQUE (message_id, ordinal)
);
