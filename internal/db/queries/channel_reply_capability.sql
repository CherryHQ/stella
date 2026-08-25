-- name: CreateChannelReplyCapability :one
INSERT INTO channel_reply_capability (id, channel_id, kind, ciphertext, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(channel_id), sqlc.arg(kind), sqlc.arg(ciphertext), sqlc.arg(expires_at))
RETURNING *;

-- name: GetLiveChannelReplyCapability :one
SELECT * FROM channel_reply_capability
WHERE id = sqlc.arg(id)
  AND channel_id = sqlc.arg(channel_id)
  AND expires_at > now();
