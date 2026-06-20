CREATE TABLE ctx_group_ingest_error (
    id          UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    group_id    UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    pipeline    TEXT NOT NULL,
    seq         BIGINT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_group_ingest_error_dedup ON ctx_group_ingest_error(group_id, pipeline, seq);
