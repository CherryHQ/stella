-- +goose Up
-- The group work-claim lease is retired: the public transcript now carries
-- coordination, and a live claim suspended agent-lap triage group-wide. Claims
-- are ephemeral leases (<= 24h), so dropping the table loses no durable data.
DROP TABLE ctx_group_claim;

-- Nothing ever wrote or read ctx_group_ingest_error: its two queries had no
-- callers, so every deployment carried it empty. Pure dead surface.
DROP TABLE ctx_group_ingest_error;

-- +goose Down
CREATE TABLE ctx_group_ingest_error (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    group_id UUID NOT NULL,
    pipeline TEXT NOT NULL,
    seq BIGINT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_group_ingest_error_dedup
    ON ctx_group_ingest_error (group_id, pipeline, seq);

CREATE TABLE ctx_group_claim (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    group_id UUID NOT NULL REFERENCES ctx_group_state(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    owner_agent_id TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    note TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, key)
);

CREATE INDEX idx_ctx_group_claim_group_lease_until
    ON ctx_group_claim (group_id, lease_until);
