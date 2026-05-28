-- Blocker queries.

-- name: CreateAgentTaskBlocker :one
INSERT INTO agent_task_blocker (
    id, task_id, kind, status, question, detail, created_by_run_id, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTaskBlocker :one
SELECT * FROM agent_task_blocker WHERE id = ?;

-- name: GetOpenBlockerForTask :one
SELECT * FROM agent_task_blocker
WHERE task_id = ? AND status = 'open'
LIMIT 1;

-- name: ListAgentTaskBlockersByTask :many
SELECT * FROM agent_task_blocker WHERE task_id = ? ORDER BY created_at DESC;

-- name: ResolveAgentTaskBlocker :execrows
UPDATE agent_task_blocker
SET status = 'resolved', resolution = ?, resolved_at = ?
WHERE id = ? AND status = 'open';

-- name: CancelAgentTaskBlocker :execrows
UPDATE agent_task_blocker
SET status = 'cancelled', resolved_at = ?
WHERE id = ? AND status = 'open';

-- name: AppendAgentTaskBlockerDetail :exec
UPDATE agent_task_blocker
SET detail = ?
WHERE id = ?;
