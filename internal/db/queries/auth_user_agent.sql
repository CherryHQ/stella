-- name: AssignUserAgent :exec
INSERT INTO auth_user_agent (user_id, agent_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: RemoveUserAgent :exec
DELETE FROM auth_user_agent WHERE user_id = $1 AND agent_id = $2;

-- name: ListUserAgents :many
SELECT agent_id FROM auth_user_agent WHERE user_id = $1 ORDER BY agent_id;

-- name: ListAgentUsers :many
SELECT user_id FROM auth_user_agent WHERE agent_id = $1 ORDER BY user_id;
