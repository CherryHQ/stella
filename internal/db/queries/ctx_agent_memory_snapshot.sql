-- name: GetMemorySnapshot :one
SELECT * FROM ctx_agent_memory_snapshot WHERE session_id = ? AND user_id = ? AND agent_id = ?;

-- name: CreateMemorySnapshot :one
INSERT INTO ctx_agent_memory_snapshot (session_id, user_id, agent_id, version, created_at, updated_at)
VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))
RETURNING *;

-- name: AdvanceMemorySnapshot :exec
UPDATE ctx_agent_memory_snapshot SET version = ?, updated_at = datetime('now')
WHERE session_id = ? AND user_id = ? AND agent_id = ?;
