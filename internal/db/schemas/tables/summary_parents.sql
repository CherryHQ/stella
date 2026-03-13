CREATE TABLE summary_parents (
    summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    parent_summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, parent_summary_id)
);
