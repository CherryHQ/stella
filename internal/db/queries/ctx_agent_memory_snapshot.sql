-- name: GetMemorySnapshot :one
SELECT * FROM ctx_agent_memory_snapshot WHERE session_id = $1 AND user_id = $2 AND agent_id = $3;

-- name: CreateMemorySnapshot :one
INSERT INTO ctx_agent_memory_snapshot (session_id, user_id, agent_id, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, now(), now())
RETURNING *;

-- name: AdvanceMemorySnapshot :exec
UPDATE ctx_agent_memory_snapshot SET version = $1, updated_at = now()
WHERE session_id = $2 AND user_id = $3 AND agent_id = $4;
