-- name: CreateExecutorBoot :one
INSERT INTO runtime_executor_boot (id, status)
VALUES (sqlc.arg(id), 'running')
RETURNING *;

-- name: HeartbeatExecutorBoot :execrows
UPDATE runtime_executor_boot
SET heartbeat_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND status = 'running';

-- name: LockRunningExecutorBoot :one
SELECT id FROM runtime_executor_boot
WHERE id = sqlc.arg(id) AND status = 'running'
FOR SHARE;

-- name: GetExecutorBootRecoveryState :one
SELECT status, heartbeat_at
FROM runtime_executor_boot
WHERE id = sqlc.arg(id);

-- name: DrainExecutorBoot :execrows
UPDATE runtime_executor_boot
SET status = 'drained', drained_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND status = 'running';
