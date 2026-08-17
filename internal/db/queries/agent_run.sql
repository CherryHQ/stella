-- name: CreateAgentRun :one
INSERT INTO agent_run (id, session_id, executor_boot_id, source, lease_expires_at)
VALUES (
    sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(executor_boot_id), sqlc.arg(source),
    now() + make_interval(secs => sqlc.arg(lease_seconds)::integer)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: InterruptExpiredAgentRunBySession :execrows
UPDATE agent_run
SET status = 'interrupted', terminal_reason = 'lease_expired',
    completed_at = now(), lease_expires_at = now(), updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND status = 'running'
  AND lease_expires_at <= now();

-- name: GetAgentRun :one
SELECT * FROM agent_run WHERE id = $1;

-- name: GetAgentRunBySource :one
SELECT * FROM agent_run
WHERE source = sqlc.arg(source);

-- name: GetRunningAgentRunBySession :one
SELECT * FROM agent_run
WHERE session_id = $1 AND status = 'running';

-- name: LockAgentRunOwnership :one
SELECT id FROM agent_run
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
  AND lease_expires_at > now()
FOR SHARE;

-- name: HeartbeatAgentRun :one
UPDATE agent_run
SET heartbeat_at = now(),
    lease_expires_at = now() + make_interval(secs => sqlc.arg(lease_seconds)::integer),
    updated_at = now()
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
  AND lease_expires_at > now()
RETURNING *;

-- name: RequestAgentRunAbort :execrows
UPDATE agent_run
SET abort_requested_at = now(), abort_reason = sqlc.arg(reason), updated_at = now()
WHERE id = sqlc.arg(run_id)
  AND status = 'running'
  AND abort_requested_at IS NULL;

-- name: RequestSessionAgentRunAbort :one
UPDATE agent_run
SET abort_requested_at = now(), abort_reason = sqlc.arg(reason), updated_at = now()
WHERE session_id = sqlc.arg(session_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
RETURNING *;

-- name: CompleteAgentRun :execrows
UPDATE agent_run
SET status = sqlc.arg(status), terminal_reason = sqlc.arg(reason),
    completed_at = now(), lease_expires_at = now(), updated_at = now()
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NULL
  AND lease_expires_at > now();

-- name: AbortAgentRun :execrows
UPDATE agent_run
SET status = 'aborted',
    abort_requested_at = COALESCE(abort_requested_at, now()),
    abort_reason = CASE WHEN abort_reason = '' THEN sqlc.arg(reason) ELSE abort_reason END,
    terminal_reason = CASE WHEN abort_reason = '' THEN sqlc.arg(reason) ELSE abort_reason END,
    completed_at = now(), lease_expires_at = now(), updated_at = now()
WHERE id = sqlc.arg(run_id)
  AND executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running';

-- name: ReapExpiredAgentRun :many
UPDATE agent_run
SET status = 'interrupted', terminal_reason = 'lease_expired',
    completed_at = now(), lease_expires_at = now(), updated_at = now()
WHERE id IN (
    SELECT id FROM agent_run
    WHERE status = 'running' AND lease_expires_at <= now()
    ORDER BY lease_expires_at
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ReapAbortRequestedAgentRun :many
UPDATE agent_run
SET status = 'aborted', terminal_reason = abort_reason,
    completed_at = now(), lease_expires_at = now(), updated_at = now()
WHERE id IN (
    SELECT id FROM agent_run
    WHERE status = 'running' AND abort_requested_at IS NOT NULL
    ORDER BY abort_requested_at
    LIMIT sqlc.arg(limit_count)
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ListAbortRequestedAgentRunByExecutor :many
SELECT * FROM agent_run
WHERE executor_boot_id = sqlc.arg(executor_boot_id)
  AND status = 'running'
  AND abort_requested_at IS NOT NULL
ORDER BY abort_requested_at;
