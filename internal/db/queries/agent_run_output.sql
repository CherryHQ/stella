-- name: CreateAgentRunOutput :one
-- Created when a channel turn is admitted, so the delivery record exists before
-- any byte can leave for the platform. The Run acquires execution ownership
-- first; this row never grants it.
INSERT INTO agent_run_output (
    run_id, session_id, platform, channel_id, chat_id, thread_id, reply_to
)
VALUES (
    sqlc.arg(run_id), sqlc.arg(session_id), sqlc.arg(platform), sqlc.arg(channel_id),
    sqlc.arg(chat_id), sqlc.arg(thread_id), sqlc.arg(reply_to)
)
RETURNING *;

-- name: GetAgentRunOutput :one
SELECT * FROM agent_run_output WHERE run_id = $1;

-- name: AuthorizeAgentRunOutputSend :one
-- The per-send authority for one channel turn, and the durable "an external
-- request may have started" marker in the same statement: a send whose attempt
-- could not be recorded must not happen at all. The full routing target is part
-- of the predicate, because a record for another channel or chat is not this
-- attempt's authority. `ready` means the committed output is being delivered;
-- `live` means the execution fence decides.
UPDATE agent_run_output
SET attempt_at = COALESCE(attempt_at, now()), updated_at = now()
WHERE run_id = sqlc.arg(run_id)
  AND platform = sqlc.arg(platform)
  AND channel_id = sqlc.arg(channel_id)
  AND chat_id = sqlc.arg(chat_id)
  AND thread_id = sqlc.arg(thread_id)
  AND reply_to = sqlc.arg(reply_to)
  AND state IN ('live', 'ready')
RETURNING state;

-- name: CompleteAgentRunOutput :execrows
-- Fills in the committed output and publishes it for delivery. Called in the
-- same transaction as the Run's terminal transition, so a terminal Run with an
-- unpublished reply cannot exist.
UPDATE agent_run_output
SET state = CASE WHEN state = 'live' THEN 'ready' ELSE state END,
    text = sqlc.arg(text), media = sqlc.arg(media), updated_at = now()
WHERE run_id = sqlc.arg(run_id);

-- name: SettleAgentRunOutput :execrows
-- One terminal delivery result. Repeating the same result is idempotent; a
-- different one cannot rewrite what the channel already recorded.
UPDATE agent_run_output
SET state = CASE
        WHEN sqlc.arg(state)::text = 'not_sent' AND attempt_at IS NOT NULL THEN 'unknown'
        ELSE sqlc.arg(state)
    END,
    settled_at = now(), updated_at = now()
WHERE run_id = sqlc.arg(run_id)
  AND state IN ('live', 'ready');

-- name: AppendAgentRunOutputPart :exec
INSERT INTO agent_run_output_part (run_id, event_no, chunk_no, metadata, data)
VALUES (sqlc.arg(run_id), sqlc.arg(event_no), sqlc.arg(chunk_no), sqlc.arg(metadata), sqlc.arg(data));
