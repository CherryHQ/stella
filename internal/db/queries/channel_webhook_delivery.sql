-- name: ClaimChannelWebhookDelivery :one
WITH claimed AS (
    INSERT INTO channel_webhook_delivery (id, endpoint_id, provider, delivery_id)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (endpoint_id, provider, delivery_id) DO NOTHING
    RETURNING id
)
SELECT EXISTS(SELECT 1 FROM claimed) AS claimed;

-- name: ReleaseChannelWebhookDelivery :execrows
DELETE FROM channel_webhook_delivery
WHERE endpoint_id = $1
  AND provider = $2
  AND delivery_id = $3;

-- name: DeleteExpiredChannelWebhookDeliveryForClaim :execrows
DELETE FROM channel_webhook_delivery
WHERE endpoint_id = $1
  AND provider = $2
  AND delivery_id = $3
  AND created_at < now() - interval '30 days';

-- name: DeleteExpiredChannelWebhookDelivery :execrows
DELETE FROM channel_webhook_delivery
WHERE id IN (
    SELECT id
    FROM channel_webhook_delivery
    WHERE created_at < now() - interval '30 days'
    ORDER BY created_at
    LIMIT sqlc.arg('limit')
    FOR UPDATE SKIP LOCKED
);
