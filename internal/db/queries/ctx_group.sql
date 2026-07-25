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

-- name: ListRecentGroupMessages :many
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(max_count);

-- name: ListRecentGroupMessagesBeforeSeq :many
SELECT * FROM ctx_group_message
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
SELECT gm.*
FROM ctx_group_message gm
WHERE gm.id IN (SELECT id FROM selected)
ORDER BY gm.seq ASC;

-- name: ListGroupMessagesPaginated :many
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
ORDER BY seq DESC
LIMIT sqlc.arg(limit_count) OFFSET sqlc.arg(offset_count);

-- name: CreateGroupMessage :one
INSERT INTO ctx_group_message (
  id, group_id, seq, source_channel_id, actor_type, actor_id,
  actor_display_name, platform_message_id, reply_to, platform_timestamp,
  idempotency_key, content, reasoning, agent_session_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING *;

-- name: ListLatestGroupActorDisplayNames :many
SELECT DISTINCT ON (actor_type, actor_id)
  actor_type,
  actor_id,
  actor_display_name
FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND actor_display_name IS NOT NULL
  AND btrim(actor_display_name) <> ''
ORDER BY actor_type, actor_id, seq DESC;
