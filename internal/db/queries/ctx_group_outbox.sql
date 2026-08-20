-- name: CreateGroupOutbox :one
INSERT INTO ctx_group_outbox (
  id, group_message_id, group_id, envelope, status, attempt_count, lease_until, next_attempt_at, last_error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: CreateGroupOutboxIfAbsent :exec
INSERT INTO ctx_group_outbox (
  id, group_message_id, group_id, envelope, status, attempt_count, lease_until, next_attempt_at, last_error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (group_message_id) DO NOTHING;

-- name: GetGroupOutbox :one
SELECT * FROM ctx_group_outbox WHERE id = $1;

-- name: GetGroupOutboxByMessage :one
SELECT * FROM ctx_group_outbox WHERE group_message_id = $1;

-- name: ListPendingGroupOutbox :many
SELECT * FROM ctx_group_outbox
WHERE status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now))
ORDER BY created_at ASC
LIMIT sqlc.arg(limit_count);

-- name: ListExpiredRunningGroupOutbox :many
SELECT * FROM ctx_group_outbox
WHERE status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg(now)
ORDER BY lease_until ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimPendingGroupOutbox :one
UPDATE ctx_group_outbox
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now))
RETURNING *;

-- name: ExtendRunningGroupOutboxLease :execrows
UPDATE ctx_group_outbox
SET lease_until = sqlc.arg(lease_until),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupOutboxCompleted :execrows
UPDATE ctx_group_outbox
SET status = 'completed',
    lease_until = NULL,
    next_attempt_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupOutboxFailed :execrows
UPDATE ctx_group_outbox
SET status = 'failed',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: RequeueGroupOutbox :execrows
UPDATE ctx_group_outbox
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);
