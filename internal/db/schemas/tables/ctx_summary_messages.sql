CREATE TABLE ctx_summary_messages (
    summary_id TEXT NOT NULL REFERENCES ctx_summaries(id) ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES ctx_messages(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);
