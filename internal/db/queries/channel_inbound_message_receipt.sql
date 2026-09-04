-- name: DeleteExpiredInboundMessageReceipt :execrows
DELETE FROM channel_inbound_message_receipt
WHERE expires_at <= now();

-- name: CreateInboundMessageReceipt :execrows
-- Claims one ordinary inbound platform message for the bounded retry window.
-- Zero rows means another process already accepted this physical delivery.
INSERT INTO channel_inbound_message_receipt (channel_id, chat_key, message_id, expires_at)
VALUES ($1, $2, $3, now() + interval '24 hours')
ON CONFLICT (channel_id, chat_key, message_id) DO NOTHING;

-- name: DeleteInboundMessageReceipt :exec
-- Releases a receipt only when the channel can prove it never handed the turn
-- to the coordinator, allowing the platform to retry a transient failure.
DELETE FROM channel_inbound_message_receipt
WHERE channel_id = $1 AND chat_key = $2 AND message_id = $3;
