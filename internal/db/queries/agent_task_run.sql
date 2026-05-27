-- name: CreateAgentTaskRun :one
INSERT INTO agent_task_run (
    id, user_id, task_id, agent_id, kind, purpose, status,
    session_id, result_json, error, deadline_at,
    started_at, finished_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTaskRun :one
SELECT * FROM agent_task_run WHERE id = ? AND user_id = ?;

-- name: ListRunsByTask :many
SELECT * FROM agent_task_run WHERE task_id = ? AND user_id = ?
ORDER BY created_at DESC;

-- name: GetActiveRunByTaskAndKind :one
SELECT * FROM agent_task_run
WHERE task_id = ? AND kind = ? AND status IN ('queued', 'running')
LIMIT 1;

-- name: CountActiveRunsByUser :one
SELECT count(*) FROM agent_task_run
WHERE user_id = ? AND status IN ('queued', 'running');

-- name: UpdateRunStatus :exec
UPDATE agent_task_run
SET status = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateRunStatusFrom :exec
UPDATE agent_task_run
SET status = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND status = ?;

-- name: CompleteRun :exec
UPDATE agent_task_run
SET status = 'completed', result_json = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: FailRun :exec
UPDATE agent_task_run
SET status = 'failed', error = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND user_id = ?;

-- name: StartRun :exec
UPDATE agent_task_run
SET status = 'running', started_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND status = 'queued';

-- name: InterruptStaleRuns :exec
UPDATE agent_task_run
SET status = 'interrupted', updated_at = ?
WHERE status = 'running' AND started_at < ?;

-- name: ListRunsByTaskAndKind :many
SELECT * FROM agent_task_run
WHERE task_id = ? AND kind = ? AND user_id = ?
ORDER BY created_at DESC;

-- name: GetLatestCompletedRunByTaskAndKind :one
SELECT * FROM agent_task_run
WHERE task_id = ? AND kind = ? AND status = 'completed'
ORDER BY finished_at DESC
LIMIT 1;
