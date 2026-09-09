-- name: EnsureChannelPrincipalQuota :one
INSERT INTO channel_principal_quota (
    principal_key, principal_kind, principal_id, max_rows, max_bytes
)
VALUES (
    sqlc.arg(principal_key), sqlc.arg(principal_kind), sqlc.arg(principal_id),
    sqlc.arg(max_rows)::bigint, sqlc.arg(max_bytes)::bigint
)
ON CONFLICT (principal_key) DO UPDATE
SET principal_kind = excluded.principal_kind,
    principal_id = excluded.principal_id,
    updated_at = now()
RETURNING *;

-- name: EnsureChannelDeploymentQuota :one
INSERT INTO channel_deployment_quota (channel_id, max_rows, max_bytes)
VALUES (sqlc.arg(channel_id), sqlc.arg(max_rows)::bigint, sqlc.arg(max_bytes)::bigint)
ON CONFLICT (channel_id) DO UPDATE
SET updated_at = now()
RETURNING *;

-- name: GetChannelPrincipalQuotaForUpdate :one
SELECT *
FROM channel_principal_quota
WHERE principal_key = sqlc.arg(principal_key)
FOR UPDATE;

-- name: GetChannelDeploymentQuotaForUpdate :one
SELECT *
FROM channel_deployment_quota
WHERE channel_id = sqlc.arg(channel_id)
FOR UPDATE;

-- name: GetChannelFIFOItem :one
SELECT * FROM channel_fifo_item WHERE id = sqlc.arg(id)::uuid;

-- name: GetChannelFIFOItemBySource :one
SELECT *
FROM channel_fifo_item
WHERE binding_id = sqlc.arg(binding_id)::uuid
  AND source_key = sqlc.arg(source_key);

-- name: InsertChannelFIFOItem :one
INSERT INTO channel_fifo_item (
    id, binding_id, principal_key, seq, source_key, schema_version, payload,
    payload_bytes, media_bytes, capability_bytes, byte_cost, command, args,
    expected_session_id, expected_binding_revision
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(binding_id)::uuid,
    sqlc.arg(principal_key),
    sqlc.arg(seq)::bigint,
    sqlc.arg(source_key),
    sqlc.arg(schema_version)::integer,
    sqlc.arg(payload)::jsonb,
    sqlc.arg(payload_bytes)::bigint,
    sqlc.arg(media_bytes)::bigint,
    sqlc.arg(capability_bytes)::bigint,
    sqlc.arg(byte_cost)::bigint,
    sqlc.arg(command),
    sqlc.arg(args),
    NULLIF(sqlc.arg(expected_session_id), ''),
    NULLIF(sqlc.arg(expected_binding_revision), 0)::bigint
)
ON CONFLICT (binding_id, source_key) WHERE source_key <> '' DO NOTHING
RETURNING *;

-- The candidate must be the oldest non-terminal item in its binding. A lease
-- expiry makes an unlinked claim eligible again; linked AgentRuns are never
-- reclaimed here because they follow the Run's terminal state instead of
-- replaying a model turn.
-- name: ClaimNextChannelFIFOItem :one
WITH candidate AS (
    SELECT item.id
    FROM channel_fifo_item AS item
    WHERE (
        (item.state = 'pending' AND item.next_attempt_at <= now())
        OR (item.state = 'running' AND item.lease_expires_at <= now())
    )
      AND item.run_id IS NULL
      AND NOT EXISTS (
          SELECT 1
          FROM channel_fifo_item AS prior
          WHERE prior.binding_id = item.binding_id
            AND prior.seq < item.seq
            AND prior.state NOT IN ('completed', 'rejected', 'discarded')
      )
    ORDER BY item.binding_id, item.seq
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE channel_fifo_item AS item
SET state = 'running',
    attempt = item.attempt + 1,
    lease_owner = sqlc.arg(lease_owner),
    lease_expires_at = now() + sqlc.arg(lease_seconds)::double precision * interval '1 second',
    updated_at = now()
FROM candidate
WHERE item.id = candidate.id
RETURNING item.*;

-- name: LinkChannelFIFOAgentRun :execrows
UPDATE channel_fifo_item
SET run_id = sqlc.arg(run_id)::uuid,
    updated_at = now()
FROM channel_binding
WHERE channel_fifo_item.id = sqlc.arg(id)::uuid
  AND channel_fifo_item.binding_id = channel_binding.id
  AND channel_fifo_item.state = 'running'
  AND channel_fifo_item.lease_owner = sqlc.arg(lease_owner)
  AND channel_fifo_item.attempt = sqlc.arg(attempt)::integer
  AND channel_fifo_item.lease_expires_at > clock_timestamp()
  AND channel_fifo_item.run_id IS NULL
  AND (channel_fifo_item.expected_session_id IS NULL
       OR channel_fifo_item.expected_session_id = sqlc.arg(session_id))
  AND channel_binding.revision = sqlc.arg(binding_revision)::bigint
  AND channel_binding.state = 'active';

-- name: SetChannelFIFOResult :execrows
UPDATE channel_fifo_item
SET result_text = sqlc.arg(result_text),
    result_handled = sqlc.arg(result_handled),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state = 'running'
  AND lease_owner = sqlc.arg(lease_owner);

-- name: CompleteChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = 'completed',
    lease_owner = '',
    lease_expires_at = NULL,
    released_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
  AND released_at IS NULL
RETURNING *;

-- name: RejectChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = 'rejected',
    lease_owner = '',
    lease_expires_at = NULL,
    rejected_by = sqlc.arg(rejected_by),
    rejected_reason = sqlc.arg(rejected_reason),
    released_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state IN ('pending', 'running', 'blocked')
  AND (state <> 'running' OR lease_owner = sqlc.arg(lease_owner))
  AND released_at IS NULL
RETURNING *;

-- name: RetryChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = CASE
        -- The Go item is a claim snapshot.  AgentRun linking happens in a
        -- separate admission transaction, so the database row is the only
        -- authoritative answer when deciding whether retry may replay work.
        WHEN sqlc.arg(block)::boolean OR run_id IS NOT NULL THEN 'blocked'
        ELSE 'pending'
    END,
    lease_owner = '',
    lease_expires_at = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at)::timestamptz,
    error_code = sqlc.arg(error_code),
    error_detail = sqlc.arg(error_detail),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND state = 'running'
  AND lease_owner = sqlc.arg(lease_owner)
  AND released_at IS NULL
RETURNING *;

-- Expiry recovery is intentionally limited to unlinked work.  A linked item
-- is reconciled from AgentRun and cannot be claimed as a fresh model turn.
-- name: ReapExpiredUnlinkedChannelFIFOItems :many
UPDATE channel_fifo_item
SET state = 'pending',
    lease_owner = '',
    lease_expires_at = NULL,
    error_code = 'lease_expired',
    error_detail = 'executor lease expired before AgentRun link',
    updated_at = now()
WHERE state = 'running'
  AND run_id IS NULL
  AND lease_expires_at IS NOT NULL
  AND lease_expires_at <= now()
RETURNING *;

-- name: ListLinkedChannelFIFOItemsNeedingRunRecovery :many
SELECT item.*, run.status AS run_status, run.completion_outcome AS run_completion_outcome
FROM channel_fifo_item AS item
JOIN agent_run AS run ON run.id = item.run_id
WHERE item.released_at IS NULL
  AND item.state IN ('pending', 'running', 'blocked')
  AND run.status <> 'running'
ORDER BY item.binding_id, item.seq;

-- A linked Run is never replayed.  This transition is the audit-preserving
-- fallback when its terminal outcome was failed, canceled, interrupted, or
-- unknown; an operator may later use RejectChannelFIFOItem to release it.
-- name: BlockLinkedChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = 'blocked',
    lease_owner = '',
    lease_expires_at = NULL,
    error_code = sqlc.arg(error_code),
    error_detail = sqlc.arg(error_detail),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND run_id IS NOT NULL
  AND released_at IS NULL
  AND state IN ('pending', 'running', 'blocked')
RETURNING *;

-- A delivered terminal Run proves the adapter settled successfully before a
-- crash in FIFO bookkeeping, so the item can be released without replaying
-- model work.  Quota updates remain in the caller's ordered transaction.
-- name: CompleteLinkedChannelFIFOItem :one
UPDATE channel_fifo_item
SET state = 'completed',
    lease_owner = '',
    lease_expires_at = NULL,
    released_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND run_id IS NOT NULL
  AND released_at IS NULL
  AND state IN ('pending', 'running', 'blocked')
RETURNING *;

-- name: ReleaseChannelBindingQuota :execrows
UPDATE channel_binding
SET released_rows = released_rows + 1,
    released_bytes = released_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(binding_id)::uuid
  AND accepted_rows >= released_rows + 1
  AND accepted_bytes >= released_bytes + sqlc.arg(byte_cost)::bigint;

-- name: ReleaseChannelPrincipalQuota :execrows
UPDATE channel_principal_quota
SET released_rows = released_rows + 1,
    released_bytes = released_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE principal_key = sqlc.arg(principal_key)
  AND accepted_rows >= released_rows + 1
  AND accepted_bytes >= released_bytes + sqlc.arg(byte_cost)::bigint;

-- name: ReleaseChannelDeploymentQuota :execrows
UPDATE channel_deployment_quota
SET released_rows = released_rows + 1,
    released_bytes = released_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)
  AND accepted_rows >= released_rows + 1
  AND accepted_bytes >= released_bytes + sqlc.arg(byte_cost)::bigint;

-- name: ReserveChannelBindingQuota :execrows
UPDATE channel_binding
SET accepted_rows = accepted_rows + 1,
    accepted_bytes = accepted_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE id = sqlc.arg(binding_id)::uuid
  AND accepted_rows - released_rows + 1 <= max_rows
  AND accepted_bytes - released_bytes + sqlc.arg(byte_cost)::bigint <= max_bytes;

-- name: ReserveChannelPrincipalQuota :execrows
UPDATE channel_principal_quota
SET accepted_rows = accepted_rows + 1,
    accepted_bytes = accepted_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE principal_key = sqlc.arg(principal_key)
  AND accepted_rows - released_rows + 1 <= max_rows
  AND accepted_bytes - released_bytes + sqlc.arg(byte_cost)::bigint <= max_bytes;

-- name: ReserveChannelDeploymentQuota :execrows
UPDATE channel_deployment_quota
SET accepted_rows = accepted_rows + 1,
    accepted_bytes = accepted_bytes + sqlc.arg(byte_cost)::bigint,
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)
  AND accepted_rows - released_rows + 1 <= max_rows
  AND accepted_bytes - released_bytes + sqlc.arg(byte_cost)::bigint <= max_bytes;
