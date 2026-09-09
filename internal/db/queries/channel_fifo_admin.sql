-- Operator maintenance queries for durable channel FIFO items. These queries
-- deliberately live apart from worker admission/claim queries: an operator has
-- no lease-owner authority and can only reject a blocked head after checking
-- the linked AgentRun state.

-- This projection is deliberately payload-free. It is bounded in SQL as well
-- as in the service so a maintenance command can always discover exact IDs
-- without turning a diagnostic list into a payload dump.
-- name: ListBlockedChannelFIFOSummaries :many
SELECT id, binding_id, principal_key, seq, source_key, command, state, attempt,
       run_id, next_attempt_at, error_code, error_detail, created_at, updated_at
FROM channel_fifo_item
WHERE state = 'blocked'
  AND released_at IS NULL
ORDER BY created_at, id
LIMIT LEAST(sqlc.arg(max_items)::integer, 100);

-- name: GetChannelFIFOItemForUpdate :one
SELECT *
FROM channel_fifo_item
WHERE id = sqlc.arg(id)::uuid
FOR UPDATE;

-- name: GetAgentRunForUpdate :one
SELECT *
FROM agent_run
WHERE id = sqlc.arg(id)::uuid
FOR UPDATE;

-- name: RejectBlockedChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = 'rejected',
    lease_owner = '',
    lease_expires_at = NULL,
    rejected_by = sqlc.arg(rejected_by),
    rejected_reason = sqlc.arg(rejected_reason),
    released_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state = 'blocked'
  AND released_at IS NULL
RETURNING *;

-- name: CreateChannelFIFORejection :one
INSERT INTO channel_fifo_rejection (item_id, operator_id, reason)
VALUES (sqlc.arg(item_id)::uuid, sqlc.arg(operator_id), sqlc.arg(reason))
RETURNING *;

-- name: ListChannelFIFORejections :many
SELECT *
FROM channel_fifo_rejection
WHERE item_id = sqlc.arg(item_id)::uuid
ORDER BY created_at, id;

-- A terminal item remains as an identity and audit receipt, but its immutable
-- media references must be removed before session-media orphan GC can delete
-- the referenced ctx_media row (the FK is intentionally RESTRICT).
-- name: DeleteChannelFIFOMediaForItem :execrows
DELETE FROM channel_fifo_media
WHERE item_id = sqlc.arg(item_id)::uuid;
