CREATE TABLE ctx_summary_parent (
    summary_id TEXT NOT NULL REFERENCES ctx_summary(id) ON DELETE CASCADE,
    parent_summary_id TEXT NOT NULL REFERENCES ctx_summary(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, parent_summary_id)
);
