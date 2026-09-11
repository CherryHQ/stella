-- name: CreateChannelOutbox :one
-- Idempotent on (delivery_key, operation_index): a completing run that
-- crashes before commit retries the whole completion transaction and the
-- retry must produce the same ledger.
INSERT INTO channel_outbox (run_id, delivery_key, operation_index, operation_kind, channel_id, source_account_key, address, payload, depends_on, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (delivery_key, operation_index) DO NOTHING
RETURNING *;

-- name: GetChannelOutboxByDelivery :many
SELECT * FROM channel_outbox
WHERE delivery_key = $1
ORDER BY operation_index;

-- name: ListPendingChannelOutbox :many
-- Owner send loop: due pending ops for channels it currently owns. An op is
-- not due while any same-delivery dependency it names is unsent — split
-- replies keep platform order and a blocked head never lets a tail jump past.
SELECT * FROM channel_outbox
WHERE channel_outbox.channel_id = $1 AND channel_outbox.state = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
  AND NOT EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(channel_outbox.depends_on) AS dep(dep_idx)
    JOIN channel_outbox AS d
      ON d.delivery_key = channel_outbox.delivery_key
     AND d.operation_index = dep.dep_idx::int
    WHERE d.state != 'sent'
  )
ORDER BY delivery_key, operation_index
LIMIT 100
FOR UPDATE SKIP LOCKED;

-- name: ClaimChannelOutboxAttempt :execrows
-- pending -> sending under a fresh attempt_token + the claimer's channel
-- runtime token. The sender re-validates the runtime token before every
-- external call, so a fenced-out owner never performs the send.
UPDATE channel_outbox
SET state = 'sending', attempt_token = sqlc.arg(attempt_token),
    owner_token = sqlc.arg(owner_token), attempt_started_at = clock_timestamp(),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND state = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp());

-- name: CompleteChannelOutboxAttempt :execrows
-- Attempt outcome, fenced by attempt_token: sent / failed / unknown. A stale
-- attempt writes nothing.
UPDATE channel_outbox
SET state = sqlc.arg(state), platform_message_id = sqlc.arg(platform_message_id),
    error_code = sqlc.arg(error_code), next_attempt_at = sqlc.arg(next_attempt_at),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND attempt_token = sqlc.arg(attempt_token) AND state = 'sending';

-- name: ListExpiredChannelOutboxAttempts :many
-- Sending attempts whose deadline passed without a recorded outcome. The
-- reaper moves them to 'unknown' (probe) rather than resending blindly.
SELECT * FROM channel_outbox
WHERE state = 'sending' AND attempt_started_at <= clock_timestamp() - interval '5 minutes'
ORDER BY attempt_started_at
LIMIT 100
FOR UPDATE SKIP LOCKED;

-- name: RequeueChannelOutboxAttempt :execrows
-- Probe decided the send did not land: back to pending with a new schedule.
UPDATE channel_outbox
SET state = 'pending', attempt_token = NULL, next_attempt_at = sqlc.arg(next_attempt_at),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND state = 'unknown';

-- name: CancelChannelOutboxByRun :execrows
-- Session/run teardown cancels everything still deliverable for the run.
UPDATE channel_outbox
SET state = 'canceled', updated_at = clock_timestamp()
WHERE run_id = $1 AND state IN ('pending', 'unknown');

-- name: MarkChannelOutboxAttemptUnknown :execrows
-- Reaper: an attempt that outlived its deadline can no longer report cleanly.
UPDATE channel_outbox
SET state = 'unknown', updated_at = clock_timestamp()
WHERE id = $1 AND state = 'sending';
