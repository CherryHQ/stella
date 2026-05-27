-- name: CreateAgentTask :one
INSERT INTO agent_task (
    id, parent_id, root_id, task_type, title, description, status, priority,
    required, retry_count, max_retries, review_policy,
    session_id, context, review_request,
    scheduler_job_id, scheduler_run_id,
    assignee_agent_id, created_by_agent_id, user_id,
    created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTask :one
SELECT * FROM agent_task WHERE id = ? AND user_id = ?;

-- name: GetAgentTaskByID :one
SELECT * FROM agent_task WHERE id = ?;

-- name: ListAgentTasksByUser :many
SELECT * FROM agent_task WHERE user_id = ? ORDER BY created_at DESC;

-- name: ListAgentTasksByUserAndAgent :many
SELECT * FROM agent_task WHERE user_id = ? AND assignee_agent_id = ? ORDER BY created_at DESC;

-- name: ListChildTasks :many
SELECT * FROM agent_task WHERE parent_id = ? AND user_id = ? ORDER BY created_at ASC;

-- name: ListTasksByRootID :many
SELECT * FROM agent_task WHERE root_id = ? AND user_id = ? ORDER BY created_at ASC;

-- name: ListReadyAgentTasks :many
SELECT * FROM agent_task WHERE status = 'ready' ORDER BY created_at ASC;

-- name: ListDraftAgentTasksWithDeps :many
SELECT DISTINCT t.* FROM agent_task t
JOIN agent_task_dep d ON d.task_id = t.id
WHERE t.status = 'draft' AND t.parent_id IS NOT NULL
ORDER BY t.created_at ASC;

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
SET title = ?, description = ?, priority = ?, assignee_agent_id = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateAgentTaskStatus :exec
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateAgentTaskStatusFrom :exec
UPDATE agent_task
SET status = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?;

-- name: UpdateAgentTaskContext :exec
UPDATE agent_task
SET context = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateAgentTaskReviewRequest :exec
UPDATE agent_task
SET review_request = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateAgentTaskNotifyAt :exec
UPDATE agent_task
SET notify_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateAgentTaskRetryCount :exec
UPDATE agent_task
SET retry_count = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: ActivateDraftChildren :exec
UPDATE agent_task
SET status = 'ready', updated_at = ?
WHERE parent_id = ? AND user_id = ? AND status = 'draft'
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_dep d
    JOIN agent_task dep ON dep.id = d.dep_id
    WHERE d.task_id = agent_task.id AND dep.status != 'done'
  );

-- name: ActivateEligibleDrafts :exec
UPDATE agent_task
SET status = 'ready', updated_at = ?
WHERE status = 'draft' AND parent_id IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM agent_task g
    WHERE g.id = agent_task.parent_id AND g.status IN ('ready', 'running')
  )
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_dep d
    JOIN agent_task dep ON dep.id = d.dep_id
    WHERE d.task_id = agent_task.id AND dep.status != 'done'
  );

-- name: ListUnblockedAgentTasks :many
SELECT t.* FROM agent_task t
WHERE t.status = 'ready'
  AND t.user_id = ?
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_dep d
    JOIN agent_task dep ON dep.id = d.dep_id
    WHERE d.task_id = t.id AND dep.status != 'done'
  )
ORDER BY t.created_at ASC;

-- name: DeleteAgentTask :exec
DELETE FROM agent_task WHERE id = ? AND user_id = ?;
