-- +goose Up
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

-- +goose Down
DROP TABLE ctx_group_claim;
