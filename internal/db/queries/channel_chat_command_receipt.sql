-- name: CreateChatCommandReceipt :execrows
-- Claims the right to execute one inbound private-chat message's command. Zero
-- rows means the claim was already taken, i.e. this message is a redelivery and
-- the command must not run again.
INSERT INTO channel_chat_command_receipt (channel_id, chat_key, message_id, command, binding)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (channel_id, chat_key, message_id) DO NOTHING;

-- name: DeleteChatCommandReceipt :exec
-- Releases a claim whose command did not run, so the next redelivery may retry.
-- This is the ONLY delete; a consumed receipt is permanent, because the Web API
-- promises message-id idempotency with no time window and a TTL here would
-- quietly reopen the destructive replay this receipt exists to prevent.
DELETE FROM channel_chat_command_receipt
WHERE channel_id = $1 AND chat_key = $2 AND message_id = $3;
