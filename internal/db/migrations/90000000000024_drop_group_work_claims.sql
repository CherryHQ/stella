-- +goose Up
-- The group work-claim lease is retired: the public transcript now carries
-- coordination, and a live claim suspended agent-lap triage group-wide. Claims
-- are ephemeral leases (<= 24h), so dropping the table loses no durable data.
DROP TABLE ctx_group_claim;

-- +goose Down
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
