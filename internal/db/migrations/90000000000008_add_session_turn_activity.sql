-- +goose Up
ALTER TABLE ctx_conversation
    ADD COLUMN last_turn_started_at TIMESTAMPTZ,
    ADD COLUMN last_turn_completed_at TIMESTAMPTZ,
    ADD COLUMN last_turn_result TEXT,
    ADD COLUMN last_viewed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE ctx_conversation
    DROP COLUMN last_viewed_at,
    DROP COLUMN last_turn_result,
    DROP COLUMN last_turn_completed_at,
    DROP COLUMN last_turn_started_at;
