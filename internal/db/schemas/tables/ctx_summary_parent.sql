CREATE TABLE ctx_summary_parent (
    summary_id TEXT NOT NULL REFERENCES ctx_summary(id) ON DELETE CASCADE,
    parent_summary_id TEXT NOT NULL REFERENCES ctx_summary(id) ON DELETE RESTRICT,
    ordinal BIGINT NOT NULL,
    PRIMARY KEY (summary_id, parent_summary_id)
);

CREATE INDEX idx_ctx_summary_parent_parent ON ctx_summary_parent(parent_summary_id, ordinal);
