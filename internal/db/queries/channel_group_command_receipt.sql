-- name: CreateGroupCommandReceipt :execrows
-- Claims the right to execute one inbound message's group command. Zero rows
-- means the claim was already taken, i.e. this message is a redelivery and the
-- command must not run again.
INSERT INTO channel_group_command_receipt (group_id, platform, message_id, command)
VALUES ($1, $2, $3, $4)
ON CONFLICT (group_id, platform, message_id) DO NOTHING;

-- name: DeleteGroupCommandReceipt :exec
-- Releases a claim whose command did not run, so the next redelivery may retry.
-- This is the ONLY delete: a consumed receipt lives until its group is deleted
-- (ON DELETE CASCADE). Ordinary group messages dedup against ctx_group_message,
-- which is also permanent, and the Web API promises client_message_id
-- idempotency with no time window — a TTL here would quietly reopen the
-- destructive replay the receipt exists to prevent. Volume is one row per
-- executed command, so permanence costs nothing.
DELETE FROM channel_group_command_receipt
WHERE group_id = $1 AND platform = $2 AND message_id = $3;
