-- +goose Up
ALTER TABLE agent_goal ADD COLUMN budget_bonus integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent_goal DROP COLUMN budget_bonus;
