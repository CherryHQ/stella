-- name: AddChannelFIFOMedia :one
INSERT INTO channel_fifo_media (item_id, media_id, file_name, mime_type, size_bytes)
VALUES (
    sqlc.arg(item_id)::uuid,
    sqlc.arg(media_id)::uuid,
    sqlc.arg(file_name),
    sqlc.arg(mime_type),
    sqlc.arg(size_bytes)::bigint
)
ON CONFLICT (item_id, media_id) DO NOTHING
RETURNING *;

-- name: ListChannelFIFOMedia :many
SELECT *
FROM channel_fifo_media
WHERE item_id = sqlc.arg(item_id)::uuid
ORDER BY media_id;

-- name: LinkChatCommandReceipt :execrows
UPDATE channel_chat_command_receipt
SET fifo_item_id = sqlc.arg(fifo_item_id)::uuid,
    expected_session_id = NULLIF(sqlc.arg(expected_session_id), ''),
    binding_revision = NULLIF(sqlc.arg(binding_revision), 0),
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)
  AND chat_key = sqlc.arg(chat_key)
  AND message_id = sqlc.arg(message_id)
  AND fifo_item_id IS NULL;
