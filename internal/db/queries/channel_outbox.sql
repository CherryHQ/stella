-- name: CreateChannelOutbox :one
-- Idempotent on (delivery_key, operation_index): a completing run that
-- crashes before commit retries the whole completion transaction and the
-- retry must produce the same ledger.
INSERT INTO channel_outbox (run_id, delivery_key, operation_index, operation_kind, channel_id, source_account_key, address, payload, depends_on, next_attempt_at, group_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (delivery_key, operation_index) DO NOTHING
RETURNING *;

-- name: ChannelOutboxOpMatches :one
-- Dedup verification compares JSONB semantically — key order and whitespace
-- normalize on write — and only the frozen identity columns: never state,
-- tokens, or scheduling fields a live send mutates.
SELECT EXISTS (
    SELECT 1 FROM channel_outbox
    WHERE delivery_key = sqlc.arg(delivery_key) AND operation_index = sqlc.arg(operation_index)
      AND operation_kind = sqlc.arg(operation_kind) AND channel_id = sqlc.arg(channel_id)
      AND source_account_key = sqlc.arg(source_account_key)
      AND run_id IS NOT DISTINCT FROM sqlc.arg(run_id)
      AND group_id IS NOT DISTINCT FROM sqlc.arg(group_id)
      AND address = sqlc.arg(address)::jsonb AND payload = sqlc.arg(payload)::jsonb
      AND depends_on = sqlc.arg(depends_on)::jsonb
);

-- name: CreateChannelOutboxAttachment :exec
-- File body for one send_attachment op, written with the op in the same
-- transaction. A dedup hit on the op never reaches here — the append path
-- verifies the existing row instead of overwriting bytes.
INSERT INTO channel_outbox_attachment (outbox_id, data)
VALUES ($1, $2);

-- name: GetChannelOutboxAttachment :one
-- Send-boundary lazy read: the op's own row id addresses its bytes.
SELECT data FROM channel_outbox_attachment
WHERE outbox_id = $1;

-- name: GetChannelOutboxAttachmentByKey :one
-- Dedup verification reads the stored body through the op's natural key.
SELECT a.data FROM channel_outbox_attachment a
JOIN channel_outbox o ON o.id = a.outbox_id
WHERE o.delivery_key = $1 AND o.operation_index = $2;

-- name: GetChannelOutboxByDelivery :many
SELECT * FROM channel_outbox
WHERE delivery_key = $1
ORDER BY operation_index;

-- name: ListPendingChannelOutbox :many
-- Owner send loop: due pending ops for channels it currently owns. An op is
-- not due while any same-delivery dependency it names is unsent — split
-- replies keep platform order and a blocked head never lets a tail jump past.
-- Dependency semantics differ by kind: a draft_update snapshot is
-- self-contained, never a delta, so a resolved predecessor — sent, failed,
-- canceled, or unknown — satisfies it. The claim-time seq fence
-- (MaxSentDraftSeq + run liveness) then decides send-vs-cancel, so a failed
-- or expired predecessor can never deadlock the run's terminal reply behind
-- an unclaimable draft. 'unknown' releases the successor for liveness only;
-- it does NOT prove the attempt is dead — a zombie edit may still land on
-- the old message, so LatestSentDraftMessageID abandons that identity.
-- Non-draft ops keep the strict 'sent' rule — their chunks are deltas and
-- must not overtake.
-- A non-draft op is also held while any same-run draft is still pending or
-- sending: the terminal reply must never be overtaken by an older progress
-- edit landing late on the same platform message.
SELECT * FROM channel_outbox
WHERE channel_outbox.channel_id = $1 AND channel_outbox.state = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= clock_timestamp())
  AND NOT EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(channel_outbox.depends_on) AS dep(dep_idx)
    JOIN channel_outbox AS d
      ON d.delivery_key = channel_outbox.delivery_key
     AND d.operation_index = dep.dep_idx::int
    WHERE CASE
          WHEN channel_outbox.operation_kind = 'draft_update'
            THEN d.state IN ('pending', 'sending')
          ELSE d.state != 'sent'
          END
  )
  AND (
    channel_outbox.operation_kind = 'draft_update'
    OR NOT EXISTS (
      SELECT 1
      FROM channel_outbox AS live
      WHERE live.run_id = channel_outbox.run_id
        AND live.operation_kind = 'draft_update'
        AND live.state IN ('pending', 'sending')
    )
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

-- name: CancelBlockedChannelOutbox :execrows
-- Sweep inside the claim transaction: a pending op whose predecessor
-- permanently failed or was canceled can never complete its delivery — the
-- chain is already broken — so cancel it instead of parking it forever.
-- draft_update is excluded: its resolved-dependency rule already releases the
-- successor, and the claim-time seq fence decides send-vs-cancel there.
UPDATE channel_outbox AS o
SET state = 'canceled', error_code = 'dependency_failed', updated_at = clock_timestamp()
WHERE o.channel_id = $1 AND o.state = 'pending' AND o.operation_kind != 'draft_update'
  AND EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(o.depends_on) AS dep(dep_idx)
    JOIN channel_outbox AS d
      ON d.delivery_key = o.delivery_key
     AND d.operation_index = dep.dep_idx::int
    WHERE d.state IN ('failed', 'canceled')
  );

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

-- name: GetLatestChannelOutboxOp :one
-- Live-draft coalescing reads the delivery's newest op to decide between an
-- in-place payload merge and an append.
SELECT * FROM channel_outbox
WHERE delivery_key = $1
ORDER BY operation_index DESC
LIMIT 1;

-- name: UpdatePendingChannelOutboxPayload :execrows
-- Coalesce: merge a newer snapshot into a draft op not yet sent, so the
-- platform never sees intermediate versions that were still queued.
UPDATE channel_outbox
SET payload = $2, updated_at = clock_timestamp()
WHERE id = $1 AND state = 'pending';

-- name: MaxSentDraftSeq :one
-- Stale-draft fence: the highest event sequence a sent draft_update covered.
-- A pending op whose snapshot seq is at or below it can never add anything.
SELECT COALESCE(MAX((payload->>'seq')::bigint), 0)::bigint FROM channel_outbox
WHERE delivery_key = $1 AND operation_kind = 'draft_update' AND state = 'sent';

-- name: LatestSentDraftMessageID :one
-- Stable draft identity for one run: the platform message id recorded by the
-- newest sent draft_update. Edits and the terminal reply reuse it — but only
-- while that message's tail of the chain is clean. An 'unknown' draft op
-- claimed after the message was sent may still have an edit in flight
-- (SIGSTOP'd owner, delayed platform apply): its outcome is unverified, not
-- proven dead. Reusing the polluted identity would let the zombie overwrite
-- the terminal reply, so the query returns '' and every later op moves to a
-- fresh message. The abandoned preview may linger or land late; the final
-- reply lives on a different identity and can never be reverted by it.
SELECT CASE WHEN EXISTS (
  SELECT 1
  FROM channel_outbox AS u
  JOIN channel_outbox AS m
    ON m.run_id = u.run_id AND m.operation_kind = 'draft_update'
   AND m.state = 'sent' AND m.platform_message_id IS NOT NULL
   AND m.operation_index < u.operation_index
  WHERE u.run_id = $1 AND u.operation_kind = 'draft_update' AND u.state = 'unknown'
    AND NOT EXISTS (
      SELECT 1 FROM channel_outbox AS n
      WHERE n.run_id = u.run_id AND n.operation_kind = 'draft_update'
        AND n.state = 'sent' AND n.platform_message_id IS NOT NULL
        AND n.operation_index > u.operation_index
    )
) THEN ''::text ELSE COALESCE((
  SELECT platform_message_id FROM channel_outbox
  WHERE run_id = $1 AND operation_kind = 'draft_update' AND state = 'sent'
    AND platform_message_id IS NOT NULL
  ORDER BY operation_index DESC
  LIMIT 1
), '')::text
END;
