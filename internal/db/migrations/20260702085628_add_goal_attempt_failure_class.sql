-- +goose Up
ALTER TABLE agent_goal_attempt
    ADD COLUMN failure_class text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_goal_attempt
    DROP COLUMN failure_class;
