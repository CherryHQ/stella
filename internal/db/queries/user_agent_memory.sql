-- name: GetUserAgentMemory :one
SELECT * FROM user_agent_memory WHERE user_id = ? AND agent_id = ?;

-- name: UpsertUserAgentMemory :exec
INSERT INTO user_agent_memory (user_id, agent_id, content, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    content = excluded.content,
    updated_at = datetime('now');

-- name: DeleteUserAgentMemory :exec
DELETE FROM user_agent_memory WHERE user_id = ? AND agent_id = ?;

-- name: ListUserAgentMemories :many
SELECT * FROM user_agent_memory WHERE user_id = ? ORDER BY agent_id;
