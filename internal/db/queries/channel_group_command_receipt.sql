-- name: CreateGroupCommandReceipt :execrows
-- Claims the right to execute one inbound message's group command. Zero rows
-- means the claim was already taken, i.e. this message is a redelivery and the
-- command must not run again.
INSERT INTO channel_group_command_receipt (group_id, platform, message_id, command)
VALUES ($1, $2, $3, $4)
ON CONFLICT (group_id, platform, message_id) DO NOTHING;

-- name: DeleteGroupCommandReceipt :exec
-- Releases a claim whose command did not run, so the next redelivery may retry.
DELETE FROM channel_group_command_receipt
WHERE group_id = $1 AND platform = $2 AND message_id = $3;

-- name: DeleteExpiredGroupCommandReceipt :exec
-- Bounds the table without a background sweeper. Platform redeliveries land
-- within minutes, so a day-old receipt can no longer dedup anything.
DELETE FROM channel_group_command_receipt
WHERE created_at < now() - interval '24 hours';
