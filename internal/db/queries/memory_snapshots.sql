-- name: GetMemorySnapshot :one
SELECT * FROM memory_snapshots WHERE session_id = ? AND user_id = ? AND agent_id = ?;

-- name: CreateMemorySnapshot :one
INSERT INTO memory_snapshots (session_id, user_id, agent_id, version, created_at, updated_at)
VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))
RETURNING *;

-- name: AdvanceMemorySnapshot :exec
UPDATE memory_snapshots SET version = ?, updated_at = datetime('now')
WHERE session_id = ? AND user_id = ? AND agent_id = ?;
