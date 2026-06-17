CREATE TABLE ctx_summary_message (
    summary_id TEXT NOT NULL REFERENCES ctx_summary(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL REFERENCES ctx_message(id) ON DELETE RESTRICT,
    ordinal BIGINT NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);
