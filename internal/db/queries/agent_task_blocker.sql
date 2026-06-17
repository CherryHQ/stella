-- Blocker queries.

-- name: CreateAgentTaskBlocker :one
INSERT INTO agent_task_blocker (
    id, task_id, kind, status, question, detail, created_by_run_id, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetAgentTaskBlocker :one
SELECT * FROM agent_task_blocker WHERE id = $1;

-- name: GetOpenBlockerForTask :one
SELECT * FROM agent_task_blocker
WHERE task_id = $1 AND status = 'open'
LIMIT 1;

-- name: GetLatestResolvedBlockerForTask :one
SELECT * FROM agent_task_blocker
WHERE task_id = $1 AND status = 'resolved'
ORDER BY resolved_at DESC
LIMIT 1;

-- name: ListAgentTaskBlockersByTask :many
SELECT * FROM agent_task_blocker WHERE task_id = $1 ORDER BY created_at DESC;

-- name: ResolveAgentTaskBlocker :execrows
UPDATE agent_task_blocker
SET status = 'resolved', resolution = $1, resolved_at = $2
WHERE id = $3 AND status = 'open';

-- name: CancelAgentTaskBlocker :execrows
UPDATE agent_task_blocker
SET status = 'cancelled', resolved_at = $1
WHERE id = $2 AND status = 'open';

-- name: AppendAgentTaskBlockerDetail :exec
UPDATE agent_task_blocker
SET detail = $1
WHERE id = $2;
