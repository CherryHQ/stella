-- GroupRoute assigns the FIFO item ID as the dispatch ledger ID. Keeping that
-- identity stable lets the FIFO claim remain the only execution lease while
-- ctx_group_dispatch continues to store accept/publish facts.
-- name: EnsureChannelGroupDispatchLedger :one
INSERT INTO ctx_group_dispatch (
    id, group_message_id, group_id, agent_id, reply_channel_id,
    status, attempt_count, lease_until, next_attempt_at, last_error,
    trigger_seq, kind
)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(group_message_id)::uuid,
    sqlc.arg(group_id)::uuid,
    sqlc.arg(agent_id),
    sqlc.arg(reply_channel_id),
    'running',
    sqlc.arg(attempt_count)::bigint,
    NULL,
    NULL,
    '',
    sqlc.arg(trigger_seq)::bigint,
    sqlc.arg(kind)
)
ON CONFLICT (id) DO UPDATE
SET updated_at = ctx_group_dispatch.updated_at
RETURNING *;

-- A FIFO retry may encounter a ledger left pending by a pre-FIFO legacy
-- attempt. Promote that row under its stable FIFO identity; terminal rows stay
-- terminal and are handled by the caller without replaying the model turn.
-- name: ClaimChannelGroupDispatchLedger :one
UPDATE ctx_group_dispatch
SET status = 'running',
    attempt_count = GREATEST(attempt_count, sqlc.arg(attempt_count)::bigint),
    lease_until = NULL,
    next_attempt_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND status IN ('pending', 'running')
RETURNING *;
