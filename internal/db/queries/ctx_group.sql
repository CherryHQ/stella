-- name: GetGroupStateByTriple :one
SELECT * FROM ctx_group_state
WHERE platform = sqlc.arg(platform)
  AND platform_group_id = sqlc.arg(platform_group_id)
  AND platform_thread_id = sqlc.arg(platform_thread_id);

-- name: CreateGroupState :one
INSERT INTO ctx_group_state (id, platform, platform_group_id, platform_thread_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: BumpGroupSeq :one
UPDATE ctx_group_state
SET next_seq = next_seq + 1, updated_at = datetime('now')
WHERE id = sqlc.arg(id)
RETURNING next_seq;

-- name: GetGroupMessageByPlatformID :one
SELECT * FROM ctx_group_message
WHERE group_id = sqlc.arg(group_id)
  AND platform_message_id = sqlc.arg(platform_message_id);

-- name: GetGroupMessageByIdempotencyKey :one
SELECT * FROM ctx_group_message
WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: CreateGroupMessage :one
INSERT INTO ctx_group_message (
  id, group_id, seq, source_channel_id, actor_type, actor_id,
  platform_message_id, reply_to, platform_timestamp, idempotency_key, content
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;
