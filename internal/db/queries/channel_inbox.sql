-- name: NextChannelIngressSeq :one
-- Caller must hold the channel row lock (GetChannelForUpdate) so concurrent
-- receivers on one channel cannot interleave sequence numbers.
SELECT (COALESCE(MAX(ingress_seq), 0) + 1)::bigint FROM channel_inbox WHERE channel_id = $1;

-- name: InsertChannelInbox :one
-- Dedup on the historical receive coordinate; redelivery returns no row and
-- the caller attaches to the existing one instead of re-executing.
INSERT INTO channel_inbox (channel_id, source_account_key, event_key, event_kind, payload_version, payload, ingress_seq, chat_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (channel_id, source_account_key, event_key) DO NOTHING
RETURNING *;

-- name: GetChannelInboxByEvent :one
SELECT * FROM channel_inbox
WHERE channel_id = $1 AND source_account_key = $2 AND event_key = $3;

-- name: ListPendingChannelInbox :many
-- Router scan: due pending events of one channel in per-chat receive order.
SELECT * FROM channel_inbox
WHERE channel_id = $1
  AND state IN ('received', 'ready')
  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
ORDER BY chat_key, ingress_seq
LIMIT 100
FOR UPDATE SKIP LOCKED;

-- name: MarkChannelInboxRouted :execrows
UPDATE channel_inbox
SET state = 'routed', routed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = $1 AND state IN ('received', 'ready');

-- name: UpdateChannelInboxState :execrows
-- Defer (next_attempt_at), reject, or fail a pending event.
UPDATE channel_inbox
SET state = sqlc.arg(state), error_code = sqlc.arg(error_code),
    next_attempt_at = sqlc.arg(next_attempt_at), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND state IN ('received', 'ready');
