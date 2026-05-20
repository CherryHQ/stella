-- name: CreateAgentTask :one
INSERT INTO agent_task (
    id, title, description, status, priority, session_id, context,
    review_request, deps, scheduler_job_id, scheduler_run_id, agent_id, user_id, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTask :one
SELECT * FROM agent_task WHERE id = ?;

-- name: ListAgentTasksByUser :many
SELECT * FROM agent_task WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListAgentTasksByUserAndAgent :many
SELECT * FROM agent_task WHERE user_id = ? AND agent_id = ? ORDER BY created_at DESC;

-- name: ListPendingAgentTasks :many
SELECT * FROM agent_task WHERE status = 'pending' ORDER BY created_at ASC;

-- name: ListRunningAgentTasks :many
SELECT * FROM agent_task WHERE status = 'running' ORDER BY created_at ASC;

-- name: CountRunningAgentTasksByUser :one
SELECT count(*) FROM agent_task WHERE status = 'running' AND user_id = ?;

-- name: ListPendingNotifyTasks :many
SELECT * FROM agent_task
WHERE notify_at IS NOT NULL AND notify_at <= ?
ORDER BY notify_at ASC;

-- name: UpdateAgentTask :exec
UPDATE agent_task
SET title = ?, description = ?, priority = ?, agent_id = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateAgentTaskStatus :exec
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateAgentTaskStatusFrom :exec
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ? AND status = ?;

-- name: UpdateAgentTaskContext :exec
UPDATE agent_task
SET context = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateAgentTaskReviewRequest :exec
UPDATE agent_task
SET review_request = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateAgentTaskNotifyAt :exec
UPDATE agent_task
SET notify_at = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteAgentTask :exec
DELETE FROM agent_task WHERE id = ?;
