-- name: CreateChannelBindingFIFO :one
WITH admission_stats AS MATERIALIZED (
    SELECT
        COALESCE(MAX(binding_revision) FILTER (
            WHERE channel_id = sqlc.arg(channel_id)
              AND binding_key = sqlc.arg(binding_key)
        ), 0) + 1 AS next_binding_revision,
        COUNT(*) FILTER (
            WHERE channel_id = sqlc.arg(channel_id)
              AND binding_key = sqlc.arg(binding_key)
              AND status IN ('pending', 'running', 'blocked')
        ) AS binding_rows,
        COALESCE(SUM(payload_bytes + attachment_bytes) FILTER (
            WHERE channel_id = sqlc.arg(channel_id)
              AND binding_key = sqlc.arg(binding_key)
              AND status IN ('pending', 'running', 'blocked')
        ), 0) AS binding_bytes,
        COUNT(*) FILTER (
            WHERE principal_id = sqlc.arg(principal_id)
              AND status IN ('pending', 'running', 'blocked')
        ) AS principal_rows,
        COALESCE(SUM(payload_bytes + attachment_bytes) FILTER (
            WHERE principal_id = sqlc.arg(principal_id)
              AND status IN ('pending', 'running', 'blocked')
        ), 0) AS principal_bytes,
        COUNT(*) FILTER (
            WHERE status IN ('pending', 'running', 'blocked')
        ) AS deployment_rows,
        COALESCE(SUM(payload_bytes + attachment_bytes) FILTER (
            WHERE status IN ('pending', 'running', 'blocked')
        ), 0) AS deployment_bytes
    FROM channel_binding_fifo
)
INSERT INTO channel_binding_fifo (
    id, channel_id, binding_key, principal_id, source_key, kind, payload, immutable_media,
    payload_bytes, attachment_bytes, expected_session_id, binding_revision,
    source_dispatch_id, source_responder_agent_id
)
SELECT sqlc.arg(id), sqlc.arg(channel_id), sqlc.arg(binding_key), sqlc.arg(principal_id),
       sqlc.arg(source_key), sqlc.arg(kind), sqlc.arg(payload), sqlc.arg(immutable_media),
       pg_column_size(sqlc.arg(payload)::jsonb::text), sqlc.arg(attachment_bytes)::bigint,
       NULLIF(sqlc.arg(expected_session_id), ''),
       admission_stats.next_binding_revision,
       NULLIF(sqlc.arg(source_dispatch_id), ''::text)::uuid,
       NULLIF(sqlc.arg(source_responder_agent_id), '')
FROM admission_stats
WHERE
    -- Completed/rejected rows remain durable receipts but consume no live
    -- admission budget. One global advisory lock makes all three quota checks
    -- authoritative even when contenders target different bindings.
    admission_stats.binding_rows < 128
AND admission_stats.binding_bytes
       + pg_column_size(sqlc.arg(payload)::jsonb::text) + sqlc.arg(attachment_bytes)::bigint <= 268435456::bigint
AND admission_stats.principal_rows < 512
AND admission_stats.principal_bytes
       + pg_column_size(sqlc.arg(payload)::jsonb::text) + sqlc.arg(attachment_bytes)::bigint <= 1073741824::bigint
AND admission_stats.deployment_rows < 4096
AND admission_stats.deployment_bytes
       + pg_column_size(sqlc.arg(payload)::jsonb::text) + sqlc.arg(attachment_bytes)::bigint <= 8589934592::bigint
ON CONFLICT (channel_id, source_key) DO UPDATE
SET updated_at = channel_binding_fifo.updated_at
-- A terminal receipt has had its content cleared, so redelivery of an
-- already-consumed source_key deduplicates on identity alone; only live rows
-- still require the redelivered content to match exactly.
WHERE channel_binding_fifo.status IN ('completed', 'rejected')
   OR (channel_binding_fifo.binding_key = excluded.binding_key
  AND channel_binding_fifo.kind = excluded.kind
  AND channel_binding_fifo.payload = excluded.payload
  AND channel_binding_fifo.immutable_media = excluded.immutable_media
  AND channel_binding_fifo.principal_id = excluded.principal_id
  AND channel_binding_fifo.attachment_bytes = excluded.attachment_bytes
  AND channel_binding_fifo.expected_session_id IS NOT DISTINCT FROM excluded.expected_session_id
  AND channel_binding_fifo.source_dispatch_id IS NOT DISTINCT FROM excluded.source_dispatch_id
  AND channel_binding_fifo.source_responder_agent_id IS NOT DISTINCT FROM excluded.source_responder_agent_id)
RETURNING *;

-- name: LockChannelBindingFIFOAdmission :exec
-- One deployment-global lock serializes every admission; shard it per
-- principal (keeping only the deployment quota tier global) if ingest
-- throughput ever matters.
-- This must be a statement before CreateChannelBindingFIFO in one transaction.
-- Under READ COMMITTED, contenders that wait here receive a fresh statement
-- snapshot for the following aggregate; taking the lock inside the INSERT's CTE
-- would leave a waiter using the stale snapshot captured before it blocked.
SELECT pg_advisory_xact_lock(hashtextextended('channel-fifo-admission', 0));

-- name: GetChannelBindingFIFOBySource :one
SELECT * FROM channel_binding_fifo
WHERE channel_id = sqlc.arg(channel_id) AND source_key = sqlc.arg(source_key);

-- name: GetChannelBindingFIFOByDispatch :one
SELECT * FROM channel_binding_fifo
WHERE source_dispatch_id = sqlc.arg(source_dispatch_id);

-- name: GetChannelBindingFIFO :one
SELECT * FROM channel_binding_fifo WHERE id = $1;

-- name: ClaimChannelBindingFIFOHead :one
UPDATE channel_binding_fifo item
SET status = 'running', attempt_count = attempt_count + 1,
    claim_token = gen_random_uuid(), claim_expires_at = now() + interval '30 seconds',
    blocked_reason = '', next_attempt_at = NULL, updated_at = now()
WHERE item.id = sqlc.arg(id)
  AND (item.status = 'pending' OR
       (item.status = 'running' AND item.run_id IS NULL AND item.claim_expires_at <= now()))
  AND (item.next_attempt_at IS NULL OR item.next_attempt_at <= now())
  AND NOT EXISTS (
      SELECT 1 FROM channel_binding_fifo head
      WHERE head.channel_id = item.channel_id
        AND head.binding_key = item.binding_key
        AND head.enqueue_seq < item.enqueue_seq
        AND head.status IN ('pending', 'running', 'blocked')
  )
RETURNING item.*;

-- name: BlockChannelBindingFIFO :execrows
UPDATE channel_binding_fifo
SET status = 'blocked', blocked_reason = sqlc.arg(reason),
    claim_token = NULL, claim_expires_at = NULL,
    -- Five failed claims exhaust automatic retry. The poison head remains an
    -- observable ordering barrier until an operator performs the explicit,
    -- attributed RejectChannelBindingFIFO transition; it is never silently
    -- skipped or dead-lettered by a replica.
    next_attempt_at = CASE
        WHEN attempt_count >= 5 THEN NULL
        ELSE now() + make_interval(secs => sqlc.arg(backoff_seconds)::integer)
    END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND status IN ('running', 'blocked');

-- name: RetryBlockedChannelBindingFIFO :execrows
UPDATE channel_binding_fifo
SET status = 'pending', updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'blocked'
  AND attempt_count < 5
  AND next_attempt_at IS NOT NULL
  AND next_attempt_at <= now();

-- name: LinkChannelBindingFIFORun :execrows
UPDATE channel_binding_fifo
SET run_id = sqlc.arg(run_id), claim_expires_at = 'infinity', updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running' AND run_id IS NULL
  AND claim_token = sqlc.arg(claim_token)
  AND claim_expires_at > now();

-- Terminal transitions clear the message content: a completed or rejected row
-- is a durable identity receipt for deduplication, not a message archive, and
-- payloads may reach 32 MiB. The empty shape matches the /new backfill and
-- still satisfies every canonical-payload constraint. Terminal receipt rows
-- themselves are kept forever; add retention pruning if row count matters.

-- name: RejectChannelBindingFIFO :execrows
UPDATE channel_binding_fifo
SET status = 'rejected', blocked_reason = sqlc.arg(reason), rejected_by = sqlc.arg(rejected_by),
    rejected_at = now(),
    payload = '[]'::jsonb, immutable_media = '[]'::jsonb,
    payload_bytes = pg_column_size('[]'::jsonb::text), attachment_bytes = 0,
    next_attempt_at = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND status IN ('pending', 'running', 'blocked');

-- name: CompleteChannelBindingFIFO :execrows
UPDATE channel_binding_fifo
SET status = 'completed', run_id = sqlc.arg(run_id),
    payload = '[]'::jsonb, immutable_media = '[]'::jsonb,
    payload_bytes = pg_column_size('[]'::jsonb::text), attachment_bytes = 0,
    next_attempt_at = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running' AND run_id = sqlc.arg(run_id);

-- name: CompleteChannelBindingFIFOControl :execrows
UPDATE channel_binding_fifo
SET status = 'completed',
    payload = '[]'::jsonb, immutable_media = '[]'::jsonb,
    payload_bytes = pg_column_size('[]'::jsonb::text), attachment_bytes = 0,
    next_attempt_at = NULL, claim_token = NULL, claim_expires_at = NULL, updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND run_id IS NULL
  AND claim_token = sqlc.arg(claim_token);

-- name: ValidateChannelBindingFIFORotation :one
-- RotateInfo executes this check in the same transaction that archives the
-- predecessor and creates its successor. Both the accepted FIFO revision and
-- the Session coordinate are immutable compare-and-rotate inputs; the claim
-- token prevents a stale command worker from borrowing a newer claim.
SELECT EXISTS (
    SELECT 1
    FROM channel_binding_fifo
    WHERE id = sqlc.arg(fifo_id)::uuid
      AND channel_id = sqlc.arg(channel_id)
      AND binding_key = sqlc.arg(binding_key)
      AND binding_revision = sqlc.arg(binding_revision)
      AND expected_session_id = sqlc.arg(expected_session_id)
      AND status = 'running'
      AND run_id IS NULL
      AND claim_token = sqlc.arg(claim_token)::uuid
      AND claim_expires_at > now()
);

-- name: ListLiveChannelBindingFIFO :many
-- Operator diagnostics: every item still occupying admission budget. Payload
-- and media are deliberately excluded — a poison head can carry 32 MiB. A
-- blocked row with next_attempt_at NULL has exhausted automatic retry and
-- waits for an operator's `stellad runtime fifo reject`.
SELECT id, enqueue_seq, channel_id, binding_key, principal_id, source_key, kind,
       payload_bytes, attachment_bytes, binding_revision, status, attempt_count,
       next_attempt_at, blocked_reason, run_id, created_at, updated_at
FROM channel_binding_fifo
WHERE status IN ('pending', 'running', 'blocked')
ORDER BY channel_id, binding_key, enqueue_seq;

-- name: LatestAgentRunForChannelFIFO :one
SELECT * FROM agent_run
WHERE session_id = $1
ORDER BY created_at DESC
LIMIT 1;
