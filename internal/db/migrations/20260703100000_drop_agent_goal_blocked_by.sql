-- +goose Up
ALTER TABLE agent_goal DROP CONSTRAINT IF EXISTS agent_goal_blocked_by_check;
ALTER TABLE agent_goal DROP COLUMN blocked_by;

-- +goose Down
ALTER TABLE agent_goal ADD COLUMN blocked_by text NOT NULL DEFAULT '';
ALTER TABLE agent_goal ADD CONSTRAINT agent_goal_blocked_by_check CHECK (blocked_by = ANY (ARRAY[''::text, 'env_unavailable'::text, 'contract_conflict'::text]));
COMMENT ON COLUMN agent_goal.blocked_by IS 'Responsibility-specific blocked cause for environment or contract failures.';
