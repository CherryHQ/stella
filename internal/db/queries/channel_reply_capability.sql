-- name: CreateChannelReplyCapability :one
-- The ciphertext is written in the same transaction as the group event and
-- outbox envelope.  Replaying the append therefore cannot mint a second
-- secret row for the same opaque capability ID.
INSERT INTO channel_reply_capability (id, channel_id, kind, ciphertext, expires_at, fifo_item_id)
VALUES ($1, $2, $3, $4, $5, NULLIF(sqlc.arg(fifo_item_id), '')::uuid)
ON CONFLICT (id) DO UPDATE
SET updated_at = channel_reply_capability.updated_at
RETURNING *;

-- name: GetLiveChannelReplyCapability :one
SELECT *
FROM channel_reply_capability
WHERE id = $1
  AND channel_id = $2
  AND expires_at > clock_timestamp();

-- name: DeleteChannelReplyCapabilityForItem :execrows
DELETE FROM channel_reply_capability
WHERE fifo_item_id = sqlc.arg(fifo_item_id)::uuid;

-- name: SweepExpiredChannelReplyCapabilities :execrows
DELETE FROM channel_reply_capability capability
WHERE capability.expires_at <= clock_timestamp()
   OR (
       capability.fifo_item_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1 FROM channel_fifo_item item
           WHERE item.id = capability.fifo_item_id
             AND item.released_at IS NULL
       )
   );
