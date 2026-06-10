-- Run queries  one row per execution attempt.

-- name: CreateAgentTaskRun :one
INSERT INTO agent_task_run (
    id, task_id, goal_id, user_id, agent_id, executor_agent_id,
    kind, attempt_no, status, session_id, input, lease_expires_at, worker_id,
    started_at, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgentTaskRun :one
SELECT * FROM agent_task_run WHERE id = ?;

-- name: ListAgentTaskRunsByTask :many
SELECT * FROM agent_task_run WHERE task_id = ? ORDER BY attempt_no DESC LIMIT ? OFFSET ?;

-- name: LatestAgentTaskRunForTask :one
SELECT * FROM agent_task_run
WHERE task_id = ? AND kind = ?
ORDER BY attempt_no DESC
LIMIT 1;

-- name: NextAttemptNoForTask :one
SELECT COALESCE(MAX(attempt_no), 0) + 1 AS next_attempt
FROM agent_task_run
WHERE task_id = ? AND kind = ?;

-- name: NextAttemptNoForGoal :one
SELECT COALESCE(MAX(attempt_no), 0) + 1 AS next_attempt
FROM agent_task_run
WHERE goal_id = ? AND kind = ?;

-- name: LatestAgentTaskRunForGoal :one
SELECT * FROM agent_task_run
WHERE goal_id = ? AND kind = ?
ORDER BY attempt_no DESC
LIMIT 1;

-- name: HeartbeatAgentTaskRun :execrows
UPDATE agent_task_run
SET heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'running';

-- Flip a queued run to running. Returns affected-rows so callers can detect
-- losing a race (e.g. another tick already promoted the run).
-- name: PromoteAgentTaskRun :execrows
UPDATE agent_task_run
SET status = 'running', started_at = ?, heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
WHERE id = ? AND status = 'queued';

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
  -- finished_at mixes RFC3339 (transition service) and naive UTC strings;
  -- datetime() normalizes both before comparing.
  AND datetime(r.finished_at) >= datetime(sqlc.arg(since))
  AND (sqlc.narg(agent_id) IS NULL OR r.agent_id = sqlc.narg(agent_id))
ORDER BY r.finished_at DESC, r.id DESC
LIMIT sqlc.arg(limit_count);
