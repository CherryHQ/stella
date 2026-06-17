-- Run queries  one row per execution attempt.

-- name: CreateAgentTaskRun :one
INSERT INTO agent_task_run (
    id, task_id, goal_id, user_id, agent_id, executor_agent_id,
    kind, attempt_no, status, session_id, input, lease_expires_at, worker_id,
    started_at, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: GetAgentTaskRun :one
SELECT * FROM agent_task_run WHERE id = $1;

-- name: ListAgentTaskRunsByTask :many
SELECT * FROM agent_task_run WHERE task_id = $1 ORDER BY attempt_no DESC LIMIT $2 OFFSET $3;

-- name: LatestAgentTaskRunForTask :one
SELECT * FROM agent_task_run
WHERE task_id = $1 AND kind = $2
ORDER BY attempt_no DESC
LIMIT 1;

-- name: NextAttemptNoForTask :one
SELECT COALESCE(MAX(attempt_no), 0) + 1 AS next_attempt
FROM agent_task_run
WHERE task_id = $1 AND kind = $2;

-- name: NextAttemptNoForGoal :one
SELECT COALESCE(MAX(attempt_no), 0) + 1 AS next_attempt
FROM agent_task_run
WHERE goal_id = $1 AND kind = $2;

-- name: LatestAgentTaskRunForGoal :one
SELECT * FROM agent_task_run
WHERE goal_id = $1 AND kind = $2
ORDER BY attempt_no DESC
LIMIT 1;

-- name: HeartbeatAgentTaskRun :execrows
UPDATE agent_task_run
SET heartbeat_at = $1, lease_expires_at = $2, updated_at = $3
WHERE id = $4 AND status = 'running';

-- Flip a queued run to running. Returns affected-rows so callers can detect
-- losing a race (e.g. another tick already promoted the run).
-- name: PromoteAgentTaskRun :execrows
UPDATE agent_task_run
SET status = 'running', started_at = $1, heartbeat_at = $2, lease_expires_at = $3, updated_at = $4
WHERE id = $5 AND status = 'queued';

-- name: FinishAgentTaskRun :exec
UPDATE agent_task_run
SET status = $1, result = $2, error = $3, finished_at = $4, updated_at = $5
WHERE id = $6;

-- name: ListStaleAgentTaskRuns :many
SELECT * FROM agent_task_run
WHERE status IN ('queued','running')
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at < $1
LIMIT $2;

-- name: ListFailedInboxTaskRuns :many
SELECT
  r.id AS run_id,
  r.task_id,
  r.agent_id,
  r.error,
  r.finished_at,
  r.created_at,
  t.title,
  t.project_id
FROM agent_task_run r
LEFT JOIN agent_task t ON t.id = r.task_id
WHERE r.user_id = sqlc.arg(user_id)
  AND r.status = 'failed'
  -- finished_at is timestamptz; compare directly.
  AND r.finished_at >= sqlc.arg(since)
  -- Only surface a failed run when its task is still terminally failed; a task
  -- that retried to success leaves stale 'failed' runs that should not nag.
  -- Goal-owned runs have no task (r.task_id IS NULL) and are always kept.
  AND (r.task_id IS NULL OR t.status = 'failed')
  AND (sqlc.narg(agent_id) IS NULL OR r.agent_id = sqlc.narg(agent_id))
ORDER BY r.finished_at DESC, r.id DESC
LIMIT sqlc.arg(limit_count);
