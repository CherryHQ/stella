CREATE TABLE ctx_group_ingest_cursor (
    group_id    UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    pipeline    TEXT NOT NULL DEFAULT 'memory_ingest',
    last_seq    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(group_id, pipeline)
);
