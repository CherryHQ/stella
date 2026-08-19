-- name: GetIngestCursor :one
SELECT * FROM ctx_group_ingest_cursor
WHERE group_id = sqlc.arg(group_id) AND pipeline = sqlc.arg(pipeline);

-- name: UpsertIngestCursor :exec
INSERT INTO ctx_group_ingest_cursor (group_id, pipeline, last_seq, updated_at)
VALUES (sqlc.arg(group_id), sqlc.arg(pipeline), sqlc.arg(last_seq), now())
ON CONFLICT(group_id, pipeline) DO UPDATE SET
    last_seq = excluded.last_seq,
    updated_at = now();

-- name: CreateIngestError :exec
INSERT INTO ctx_group_ingest_error (id, group_id, pipeline, seq, reason)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT(group_id, pipeline, seq) DO NOTHING;

-- name: IsIngestError :one
SELECT count(*) > 0 as is_error FROM ctx_group_ingest_error
WHERE group_id = sqlc.arg(group_id) AND pipeline = sqlc.arg(pipeline) AND seq = sqlc.arg(seq);

-- Both ingest list queries feed text-only consumers (group-memory extraction and
-- LCM cross-agent assembly); neither reads content_blocks. They select an
-- explicit column list that EXCLUDES content_blocks so a lagging agent or a
-- memory batch never drags inbound image payloads across the seq range —
-- ListGroupMessagesBetweenSeqs has no LIMIT, so the interval alone bounds it.

-- name: ListGroupMessagesAfterSeq :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       platform_message_id, reply_to, platform_timestamp, idempotency_key,
       content, reasoning, agent_session_id, created_at, delivery_state
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id) AND seq > sqlc.arg(min_seq)
ORDER BY seq ASC
LIMIT sqlc.arg(batch_limit);

-- name: ListLatestGroupMessagesAfterSeq :many
-- Replay for a live stream wants the newest window, not the oldest: a group past
-- the batch limit would otherwise replay its first N messages forever and never
-- show the ones the client is waiting for.
SELECT * FROM (
  SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
         platform_message_id, reply_to, platform_timestamp, idempotency_key,
         content, reasoning, agent_session_id, created_at, delivery_state
  FROM ctx_group_message
  WHERE group_id = sqlc.arg(group_id) AND seq > sqlc.arg(min_seq)
  ORDER BY seq DESC
  LIMIT sqlc.arg(batch_limit)
) recent
ORDER BY recent.seq ASC;

-- name: ListGroupMessagesBetweenSeqs :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       platform_message_id, reply_to, platform_timestamp, idempotency_key,
       content, reasoning, agent_session_id, created_at
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq > sqlc.arg(after_seq)
  AND seq < sqlc.arg(before_seq)
ORDER BY seq ASC;

-- name: ListGroupsWithPendingIngest :many
SELECT gs.id as group_id,
       COALESCE(c.last_seq, 0) as cursor_seq,
       gs.next_seq as head_seq
FROM ctx_group_state gs
LEFT JOIN ctx_group_ingest_cursor c
  ON c.group_id = gs.id AND c.pipeline = sqlc.arg(pipeline)
WHERE gs.next_seq > COALESCE(c.last_seq, 0);
