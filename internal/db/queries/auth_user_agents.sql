-- name: AssignUserAgent :exec
INSERT INTO auth_user_agents (user_id, agent_id)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: RemoveUserAgent :exec
DELETE FROM auth_user_agents WHERE user_id = ? AND agent_id = ?;

-- name: ListUserAgents :many
SELECT agent_id FROM auth_user_agents WHERE user_id = ? ORDER BY agent_id;

-- name: ListAgentUsers :many
SELECT user_id FROM auth_user_agents WHERE agent_id = ? ORDER BY user_id;
