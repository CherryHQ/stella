-- +goose Up
ALTER TABLE agent_goal_attempt
    ADD COLUMN repair_rounds integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT agent_goal_attempt_repair_rounds_check CHECK (repair_rounds >= 0);

-- +goose Down
ALTER TABLE agent_goal_attempt
    DROP CONSTRAINT agent_goal_attempt_repair_rounds_check,
    DROP COLUMN repair_rounds;
