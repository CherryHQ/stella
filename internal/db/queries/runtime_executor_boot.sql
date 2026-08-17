-- name: CreateExecutorBoot :one
INSERT INTO runtime_executor_boot (id, status, control_backend_pid)
VALUES (sqlc.arg(id), 'running', sqlc.arg(control_backend_pid))
RETURNING *;

-- name: HeartbeatExecutorBoot :execrows
UPDATE runtime_executor_boot
SET heartbeat_at = now(), updated_at = now()
WHERE id = $1 AND status = 'running';

-- name: ReconnectExecutorBoot :execrows
UPDATE runtime_executor_boot
SET control_backend_pid = sqlc.arg(control_backend_pid),
    heartbeat_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running';

-- name: DrainExecutorBoot :execrows
UPDATE runtime_executor_boot
SET status = 'drained', drained_at = now(), updated_at = now()
WHERE id = $1 AND status = 'running';
