-- +goose Up
-- Deprecated rows are retained for recovery, so only live rows reserve names.
DROP INDEX idx_skill_owner_name;
CREATE UNIQUE INDEX idx_skill_owner_name
ON skill (name, scope, COALESCE(user_id::text, ''), COALESCE(agent_id, ''))
WHERE status <> 'deprecated';

-- +goose Down
-- Do not discard recoverable rows to recreate the historical all-status index.
-- If a deprecated row now shares a name with a live replacement, abort before
-- changing indexes so the operator can resolve the conflict deliberately.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM skill
    GROUP BY name, scope, COALESCE(user_id::text, ''), COALESCE(agent_id, '')
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'cannot restore idx_skill_owner_name: duplicate owner/name rows exist';
  END IF;
END $$;
-- +goose StatementEnd
DROP INDEX idx_skill_owner_name;
CREATE UNIQUE INDEX idx_skill_owner_name
ON skill (name, scope, COALESCE(user_id::text, ''), COALESCE(agent_id, ''));
