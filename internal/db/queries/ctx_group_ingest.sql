-- name: GetIngestCursor :one
SELECT * FROM ctx_group_ingest_cursor
WHERE group_id = sqlc.arg(group_id) AND pipeline = sqlc.arg(pipeline);

-- name: UpsertIngestCursor :exec
INSERT INTO ctx_group_ingest_cursor (group_id, pipeline, last_seq, updated_at)
VALUES (sqlc.arg(group_id), sqlc.arg(pipeline), sqlc.arg(last_seq), now())
ON CONFLICT(group_id, pipeline) DO UPDATE SET
    last_seq = excluded.last_seq,
    updated_at = now();

-- These text-only ingest/context readers deliberately exclude content_blocks:
-- historical group context retains the content projection (including [image])
-- without loading the original media payload.

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

-- name: ListDeliveredGroupMessagesBeforeSeq :many
-- Reverse pagination is mandatory: group context reads only its newest bounded
-- window, never an interval whose size is controlled by group history.
SELECT id, group_id, seq, actor_type, actor_id, actor_display_name, content, delivery_state
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(before_seq)
  AND (
    delivery_state = 'delivered'
    OR (actor_type = 'agent' AND actor_id = sqlc.arg(agent_id))
  )
ORDER BY seq DESC
LIMIT sqlc.arg(page_size);

-- name: ExistsGroupMessageBeforeSeq :one
SELECT EXISTS (
  SELECT 1
  FROM ctx_group_message
  WHERE group_id = sqlc.arg(group_id)
    AND seq < sqlc.arg(before_seq)
)::boolean;

-- name: GetDeliveredGroupMessageBySeq :one
SELECT id, group_id, seq, actor_type, actor_id, actor_display_name, content
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq = sqlc.arg(seq)
  AND delivery_state = 'delivered';

-- name: SearchDeliveredGroupRecall :many
-- The group and trigger sequence come only from the trusted turn context. BM25
-- indexes text and the event-time display-name snapshot, while the canonical
-- group/seq unique index narrows the visibility boundary.
SELECT id, seq, actor_type, actor_display_name, content, created_at,
       COALESCE(paradedb.snippet(content), paradedb.snippet(actor_display_name), '')::text AS snippet,
       paradedb.score(id)::double precision AS score
FROM ctx_group_message
WHERE (id @@@ paradedb.match('content', sqlc.arg('match')::text)
    OR id @@@ paradedb.match('actor_display_name', sqlc.arg('match')::text))
  AND group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(trigger_seq)
  AND delivery_state = 'delivered'
  AND btrim(content) <> ''
ORDER BY score DESC, created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: GetDeliveredGroupRecallMessage :one
SELECT id, seq, actor_type, actor_display_name, content, created_at
FROM ctx_group_message
WHERE id = sqlc.arg(id)
  AND group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(trigger_seq)
  AND delivery_state = 'delivered'
  AND btrim(content) <> '';

-- name: ListDeliveredGroupRecallBeforeSeq :many
SELECT id, seq, actor_type, actor_display_name, content, created_at
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(before_seq)
  AND delivery_state = 'delivered'
  AND btrim(content) <> ''
ORDER BY seq DESC
LIMIT sqlc.arg(limit_count);

-- name: ListDeliveredGroupRecallAfterSeq :many
SELECT id, seq, actor_type, actor_display_name, content, created_at
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq > sqlc.arg(after_seq)
  AND seq < sqlc.arg(trigger_seq)
  AND delivery_state = 'delivered'
  AND btrim(content) <> ''
ORDER BY seq ASC
LIMIT sqlc.arg(limit_count);

-- name: ListGroupsWithPendingIngest :many
SELECT gs.id as group_id,
       COALESCE(c.last_seq, 0) as cursor_seq,
       gs.next_seq as head_seq
FROM ctx_group_state gs
LEFT JOIN ctx_group_ingest_cursor c
  ON c.group_id = gs.id AND c.pipeline = sqlc.arg(pipeline)
WHERE gs.next_seq > COALESCE(c.last_seq, 0);
