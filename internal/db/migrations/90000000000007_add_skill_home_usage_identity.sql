-- +goose Up
ALTER TABLE skill_usage
  ADD COLUMN scope TEXT,
  ADD COLUMN name TEXT,
  ADD COLUMN last_content_digest TEXT;

CREATE UNIQUE INDEX idx_skill_usage_home_user_agent_identity
  ON skill_usage (user_id, agent_id, scope, name)
  WHERE scope = 'user_agent';

-- +goose Down
DROP INDEX idx_skill_usage_home_user_agent_identity;

ALTER TABLE skill_usage
  DROP COLUMN last_content_digest,
  DROP COLUMN name,
  DROP COLUMN scope;
