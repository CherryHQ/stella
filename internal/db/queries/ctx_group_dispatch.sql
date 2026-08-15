-- name: CreateGroupDispatch :exec
INSERT INTO ctx_group_dispatch (
  id, group_message_id, group_id, agent_id, reply_channel_id, status, attempt_count, lease_until, next_attempt_at, last_error
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING;

-- name: GetGroupDispatch :one
SELECT * FROM ctx_group_dispatch WHERE id = $1;

-- name: CountGroupDispatchByMessage :one
SELECT CAST(COUNT(*) AS BIGINT) FROM ctx_group_dispatch
WHERE group_message_id = $1;

-- name: CountNonTerminalGroupDispatchByMessage :one
SELECT CAST(COUNT(*) AS BIGINT) FROM ctx_group_dispatch
WHERE group_message_id = $1
  AND status IN ('pending', 'running');

-- name: ListPendingGroupDispatchByMessage :many
SELECT * FROM ctx_group_dispatch gd
WHERE gd.group_message_id = sqlc.arg(group_message_id)
  AND gd.status = 'pending'
  AND (gd.next_attempt_at IS NULL OR gd.next_attempt_at <= sqlc.arg('now'))
  AND NOT EXISTS (
    SELECT 1
    FROM ctx_group_dispatch earlier
    JOIN ctx_group_message earlier_msg ON earlier_msg.id = earlier.group_message_id
    JOIN ctx_group_message current_msg ON current_msg.id = gd.group_message_id
    WHERE earlier.group_id = gd.group_id
      AND earlier.agent_id = gd.agent_id
      AND earlier_msg.seq < current_msg.seq
      AND (
        earlier.status = 'pending'
        OR (earlier.status = 'running' AND earlier.lease_until IS NOT NULL AND earlier.lease_until >= sqlc.arg('now'))
      )
  )
  AND NOT EXISTS (
    SELECT 1
    FROM ctx_group_outbox earlier_outbox
    JOIN ctx_group_message earlier_msg ON earlier_msg.id = earlier_outbox.group_message_id
    JOIN ctx_group_message current_msg ON current_msg.id = gd.group_message_id
    WHERE earlier_outbox.group_id = gd.group_id
      AND earlier_msg.seq < current_msg.seq
      AND (
        earlier_outbox.status = 'pending'
        OR (earlier_outbox.status = 'running' AND earlier_outbox.lease_until IS NOT NULL AND earlier_outbox.lease_until >= sqlc.arg('now'))
      )
  )
ORDER BY gd.created_at ASC;

-- name: ListPendingGroupDispatch :many
SELECT * FROM ctx_group_dispatch gd
WHERE gd.status = 'pending'
  AND (gd.next_attempt_at IS NULL OR gd.next_attempt_at <= sqlc.arg('now'))
  AND NOT EXISTS (
    SELECT 1
    FROM ctx_group_dispatch earlier
    JOIN ctx_group_message earlier_msg ON earlier_msg.id = earlier.group_message_id
    JOIN ctx_group_message current_msg ON current_msg.id = gd.group_message_id
    WHERE earlier.group_id = gd.group_id
      AND earlier.agent_id = gd.agent_id
      AND earlier_msg.seq < current_msg.seq
      AND (
        earlier.status = 'pending'
        OR (earlier.status = 'running' AND earlier.lease_until IS NOT NULL AND earlier.lease_until >= sqlc.arg('now'))
      )
  )
  AND NOT EXISTS (
    SELECT 1
    FROM ctx_group_outbox earlier_outbox
    JOIN ctx_group_message earlier_msg ON earlier_msg.id = earlier_outbox.group_message_id
    JOIN ctx_group_message current_msg ON current_msg.id = gd.group_message_id
    WHERE earlier_outbox.group_id = gd.group_id
      AND earlier_msg.seq < current_msg.seq
      AND (
        earlier_outbox.status = 'pending'
        OR (earlier_outbox.status = 'running' AND earlier_outbox.lease_until IS NOT NULL AND earlier_outbox.lease_until >= sqlc.arg('now'))
      )
  )
ORDER BY gd.created_at ASC
LIMIT sqlc.arg(limit_count);
-- name: ListExpiredRunningGroupDispatch :many
SELECT * FROM ctx_group_dispatch
WHERE status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now')
ORDER BY lease_until ASC
LIMIT sqlc.arg(limit_count);

-- name: ClaimPendingGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= sqlc.arg('now'))
RETURNING *;

-- name: ClaimExpiredGroupDispatch :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = attempt_count + 1,
    lease_until = sqlc.arg(lease_until),
    next_attempt_at = NULL,
    last_error = '',
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND lease_until IS NOT NULL
  AND lease_until <= sqlc.arg('now')
RETURNING *;

-- name: ExtendRunningGroupDispatchLease :execrows
UPDATE ctx_group_dispatch
SET lease_until = sqlc.arg(lease_until),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: SetGroupDispatchResultMessage :execrows
UPDATE ctx_group_dispatch
SET result_message_id = sqlc.arg(result_message_id),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: AdvanceGroupDispatchDelivery :execrows
-- Monotonic: a publisher only ever confirms chunks it has just sent, so a
-- non-advancing cursor means this attempt no longer owns the row (the
-- attempt_count guard) or a stale confirmation arrived late. Both must read as
-- "not delivered" rather than silently rewinding a later attempt's progress.
UPDATE ctx_group_dispatch
SET delivery_cursor = sqlc.arg(delivery_cursor),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count)
  AND delivery_cursor < sqlc.arg(delivery_cursor);

-- name: ResetGroupDispatchDelivery :execrows
-- The cursor indexes the chunks of one specific response. Running the agent
-- again produces a different response, so an attempt that regenerates instead
-- of re-delivering must start from zero or it would skip real content.
UPDATE ctx_group_dispatch
SET delivery_cursor = 0,
    delivery_complete = false,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchDelivered :execrows
UPDATE ctx_group_dispatch
SET delivery_complete = true,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchCompleted :execrows
UPDATE ctx_group_dispatch
SET status = 'completed',
    lease_until = NULL,
    next_attempt_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: MarkGroupDispatchFailed :execrows
UPDATE ctx_group_dispatch
SET status = 'failed',
    lease_until = NULL,
    next_attempt_at = NULL,
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);

-- name: RequeueGroupDispatch :execrows
UPDATE ctx_group_dispatch
SET status = 'pending',
    lease_until = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.arg(last_error),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND attempt_count = sqlc.arg(attempt_count);
