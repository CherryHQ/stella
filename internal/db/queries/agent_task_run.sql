-- Run queries  one row per execution attempt.

-- name: CreateAgentTaskRun :one
INSERT INTO agent_task_run (
    id, task_id, org_id, user_id, agent_id, executor_agent_id,
    kind, attempt_no, status, session_id, input, lease_expires_at, worker_id,
    started_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTaskRun :one
SELECT * FROM agent_task_run WHERE id = ?;

-- name: ListAgentTaskRunsByTask :many
SELECT * FROM agent_task_run WHERE task_id = ? ORDER BY attempt_no DESC;

-- name: LatestAgentTaskRunForTask :one
SELECT * FROM agent_task_run
WHERE task_id = ? AND kind = ?
ORDER BY attempt_no DESC
LIMIT 1;

-- name: NextAttemptNoForTask :one
SELECT COALESCE(MAX(attempt_no), 0) + 1 AS next_attempt
FROM agent_task_run
WHERE task_id = ? AND kind = ?;

-- name: HeartbeatAgentTaskRun :execrows
UPDATE agent_task_run
SET heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'running';

-- name: FinishAgentTaskRun :exec
UPDATE agent_task_run
SET status = ?, result = ?, error = ?, finished_at = ?, updated_at = ?
WHERE id = ?;

-- name: ListStaleAgentTaskRuns :many
SELECT * FROM agent_task_run
WHERE status IN ('queued','running')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at < ?
LIMIT ?;
