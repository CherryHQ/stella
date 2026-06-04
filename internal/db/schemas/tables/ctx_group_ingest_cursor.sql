CREATE TABLE ctx_group_ingest_cursor (
    group_id    TEXT NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    pipeline    TEXT NOT NULL DEFAULT 'memory_ingest',
    last_seq    INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(group_id, pipeline)
);
