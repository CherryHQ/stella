-- name: CreateChatCommandReceipt :execrows
-- Claims the right to execute one inbound private-chat message's command. Zero
-- rows means the claim was already taken, i.e. this message is a redelivery and
-- the command must not run again.
INSERT INTO channel_chat_command_receipt (agent_id, binding, channel_id, message_id, command)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (binding, channel_id, message_id) DO NOTHING;

-- name: DeleteChatCommandReceipt :exec
-- Releases a claim whose command did not run, so the next redelivery may retry.
-- This is the ONLY delete; a consumed receipt is permanent for the same reason
-- the group receipt's is (see channel_group_command_receipt.sql).
DELETE FROM channel_chat_command_receipt
WHERE binding = $1 AND channel_id = $2 AND message_id = $3;
