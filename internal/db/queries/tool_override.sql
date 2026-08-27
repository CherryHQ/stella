-- name: GetToolOverride :one
SELECT * FROM tool_override
WHERE tool_name = sqlc.arg(tool_name)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
LIMIT 1;

-- ListToolOverridesForAgentContext returns rows visible to one user and agent.
-- name: ListToolOverridesForAgentContext :many
SELECT * FROM tool_override
WHERE scope = 'system'
   OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id))
   OR (scope = 'user'         AND user_id = sqlc.narg(user_id))
   OR (scope = 'user_agent'   AND user_id = sqlc.narg(user_id) AND agent_id = sqlc.narg(agent_id))
ORDER BY CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END, tool_name;

-- name: UpsertToolOverride :one
INSERT INTO tool_override (tool_name, scope, user_id, agent_id, enabled)
VALUES (sqlc.arg(tool_name), sqlc.arg(scope), sqlc.narg(user_id), sqlc.narg(agent_id), sqlc.arg(enabled))
ON CONFLICT (tool_name, scope, user_id, agent_id)
DO UPDATE SET enabled = excluded.enabled, updated_at = now()
RETURNING *;

-- name: DeleteToolOverride :exec
DELETE FROM tool_override
WHERE tool_name = sqlc.arg(tool_name)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '');
