-- name: GetUserAgentMemory :one
SELECT * FROM ctx_agent_memory WHERE user_id = ? AND agent_id = ?;

-- name: UpsertUserAgentMemory :exec
INSERT INTO ctx_agent_memory (user_id, agent_id, content, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(user_id, agent_id) DO UPDATE SET
    content = excluded.content,
    updated_at = datetime('now');

-- name: DeleteUserAgentMemory :exec
DELETE FROM ctx_agent_memory WHERE user_id = ? AND agent_id = ?;

-- name: ListUserAgentMemories :many
SELECT * FROM ctx_agent_memory ORDER BY user_id, agent_id;

-- name: ListUserAgentMemoriesByUser :many
SELECT * FROM ctx_agent_memory WHERE user_id = ? ORDER BY agent_id;
