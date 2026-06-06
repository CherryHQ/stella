-- name: CreateGroupDispatch :exec
INSERT OR IGNORE INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetGroupDispatch :one
SELECT * FROM ctx_group_dispatch WHERE id = ?;

-- name: CountGroupDispatchByMessage :one
SELECT CAST(COUNT(*) AS INTEGER) FROM ctx_group_dispatch
WHERE group_message_id = ?;

-- name: ListPendingGroupDispatchByMessage :many
SELECT * FROM ctx_group_dispatch
WHERE group_message_id = sqlc.arg(group_message_id)
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now))
ORDER BY created_at ASC;

-- name: ListPendingGroupDispatch :many
SELECT * FROM ctx_group_dispatch
WHERE status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now))
ORDER BY created_at ASC
LIMIT sqlc.arg(limit_count);

-- name: ListExpiredRunningGroupDispatch :many
SELECT * FROM ctx_group_dispatch
WHERE status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg(now)
ORDER BY lease_until ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimPendingGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = datetime('now')
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg(now))
RETURNING *;

-- name: ClaimExpiredGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = datetime('now')
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg(now)
RETURNING *;

-- name: MarkGroupDispatchCompleted :exec
UPDATE ctx_group_dispatch
SET status = 'completed',
    lease_until = NULL,
    next_attempt_at = NULL,
    updated_at = datetime('now')
WHERE id = ?;

-- name: MarkGroupDispatchFailed :exec
UPDATE ctx_group_dispatch
SET status = 'failed',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- name: RequeueGroupDispatch :exec
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.arg(last_error),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);
