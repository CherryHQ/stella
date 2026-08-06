-- name: GetGroupStateByTriple :one
SELECT * FROM ctx_group_state
WHERE platform = sqlc.arg(platform)
  AND platform_group_id = sqlc.arg(platform_group_id)
  AND platform_thread_id = sqlc.arg(platform_thread_id);

-- name: CreateGroupState :one
INSERT INTO ctx_group_state (id, platform, platform_group_id, platform_thread_id, group_name, created_by_user_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetGroupStateByID :one
SELECT * FROM ctx_group_state WHERE id = $1;

-- name: GetGroupStateByIDForUpdate :one
SELECT * FROM ctx_group_state WHERE id = $1 FOR UPDATE;

-- name: GetGroupLastActive :one
SELECT COALESCE(MAX(gm.created_at), gs.updated_at) AS last_active
FROM ctx_group_state gs
LEFT JOIN ctx_group_message gm ON gm.group_id = gs.id
WHERE gs.id = $1
GROUP BY gs.id;

-- name: ListGroupsByUser :many
SELECT
  gs.*,
  COALESCE(MAX(gm.created_at), gs.updated_at) AS last_active
FROM ctx_group_state gs
LEFT JOIN ctx_group_message gm ON gm.group_id = gs.id
WHERE gs.created_by_user_id = sqlc.arg(user_id)
  AND gs.platform = 'web'
GROUP BY gs.id
ORDER BY last_active DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: UpdateGroupName :one
UPDATE ctx_group_state
SET group_name = sqlc.arg(group_name), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteGroupState :exec
DELETE FROM ctx_group_state WHERE id = $1;

-- name: BumpGroupSeq :one
UPDATE ctx_group_state
SET next_seq = next_seq + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING next_seq;

-- name: GetGroupMessageByPlatformID :one
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND platform_message_id = sqlc.arg(platform_message_id);

-- name: GetGroupMessageByIdempotencyKey :one
SELECT * FROM ctx_group_message
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetGroupMessage :one
SELECT * FROM ctx_group_message WHERE id = $1;

-- content_blocks holds inbound image payloads, so a single row is bounded only
-- by the channel inline ceiling (telegram/qq inline up to 20MB today; 5MB once
-- #786 lands). Only the dispatch reads above (GetGroupMessage / the dedup
-- :one lookups) rehydrate images, so they keep SELECT *. The text-only list
-- consumers below — semantic-arbiter recent context, LCM cross-agent assembly,
-- and web pagination — read only the projected text columns, so they select an
-- explicit column list that EXCLUDES content_blocks and never drag image blobs
-- across a history window.

-- ListRecentGroupMessages currently has no caller; it keeps SELECT * so a future
-- replay/dispatch consumer that needs content_blocks gets the full row. Give it
-- an explicit lean list only if a text-only consumer adopts it.
-- name: ListRecentGroupMessages :many
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(max_count);

-- name: ListRecentGroupMessagesBeforeSeq :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       actor_display_name, platform_message_id, reply_to, platform_timestamp,
       idempotency_key, content, reasoning, agent_session_id, created_at
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND seq < sqlc.arg(before_seq)
ORDER BY seq DESC
LIMIT sqlc.arg(max_count);

-- name: ListGroupMessagesForLCM :many
WITH eligible AS (
  SELECT
    gm.id,
    gm.seq,
    CAST(GREATEST((octet_length(gm.content) + 3) / 4, 1) AS BIGINT) AS estimated_tokens
  FROM ctx_group_message gm
  WHERE gm.group_id = sqlc.arg(group_id)
    AND gm.seq > sqlc.arg(after_seq)
    AND gm.seq < sqlc.arg(before_seq)
    AND gm.content <> ''
    AND NOT (gm.actor_type = 'agent' AND gm.actor_id = sqlc.arg(self_agent_id))
    -- A single oversized public message is not useful as bootstrap context.
    AND GREATEST((octet_length(gm.content) + 3) / 4, 1) <= sqlc.arg(token_budget)::bigint
),
bounded AS (
  SELECT
    id,
    seq,
    SUM(estimated_tokens) OVER (ORDER BY seq DESC) AS running_tokens
  FROM eligible
),
selected AS (
  SELECT id
  FROM bounded
  WHERE running_tokens <= sqlc.arg(token_budget)::bigint
)
SELECT gm.id, gm.group_id, gm.seq, gm.source_channel_id,
       gm.actor_type, gm.actor_id, gm.actor_display_name,
       gm.platform_message_id, gm.reply_to, gm.platform_timestamp,
       gm.idempotency_key, gm.content, gm.reasoning,
       gm.agent_session_id, gm.created_at
FROM ctx_group_message gm
WHERE gm.id IN (SELECT id FROM selected)
ORDER BY gm.seq ASC;

-- name: ListGroupMessagesPaginated :many
SELECT id, group_id, seq, source_channel_id, actor_type, actor_id,
       actor_display_name, platform_message_id, reply_to, platform_timestamp,
       idempotency_key, content, reasoning, agent_session_id, created_at
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: CreateGroupMessage :one
INSERT INTO ctx_group_message (
  id, group_id, seq, source_channel_id, actor_type, actor_id,
  actor_display_name, platform_message_id, reply_to, platform_timestamp,
  idempotency_key, content, content_blocks, reasoning, agent_session_id
)
VALUES (
  sqlc.arg(id), sqlc.arg(group_id), sqlc.arg(seq), sqlc.arg(source_channel_id),
  sqlc.arg(actor_type), sqlc.arg(actor_id), sqlc.arg(actor_display_name),
  sqlc.arg(platform_message_id), sqlc.arg(reply_to),
  sqlc.arg(platform_timestamp), sqlc.arg(idempotency_key), sqlc.arg(content),
  COALESCE(sqlc.arg(content_blocks)::jsonb, '[]'::jsonb),
  sqlc.arg(reasoning), sqlc.arg(agent_session_id)
)
RETURNING *;
