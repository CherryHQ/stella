-- +goose Up
CREATE TABLE model_catalog (
    id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    etag TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_llm_call
    ADD COLUMN reasoning_tokens BIGINT CHECK (reasoning_tokens IS NULL OR reasoning_tokens >= 0);

-- +goose Down
ALTER TABLE agent_llm_call DROP COLUMN reasoning_tokens;
DROP TABLE model_catalog;
