-- +goose Up
ALTER TABLE agent_llm_call
    ADD COLUMN reasoning_tokens BIGINT CHECK (reasoning_tokens IS NULL OR reasoning_tokens >= 0);

-- +goose Down
ALTER TABLE agent_llm_call DROP COLUMN reasoning_tokens;
