-- name: CreateChannelWebhookEndpoint :one
INSERT INTO channel_webhook_endpoint (
    channel_id,
    owner_user_id,
    provider,
    token_public_id,
    token_hash,
    token_last4
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetChannelWebhookEndpointByChannelID :one
SELECT * FROM channel_webhook_endpoint
WHERE channel_id = $1;

-- name: GetChannelWebhookEndpointByChannelIDForUpdate :one
SELECT * FROM channel_webhook_endpoint
WHERE channel_id = $1
FOR UPDATE;

-- name: GetChannelWebhookEndpointByPublicID :one
SELECT * FROM channel_webhook_endpoint
WHERE token_public_id = $1;

-- name: RotateChannelWebhookEndpoint :one
UPDATE channel_webhook_endpoint
SET token_public_id = $2,
    token_hash = $3,
    token_last4 = $4,
    revision = revision + 1,
    rotated_at = now(),
    updated_at = now()
WHERE channel_id = $1
RETURNING *;

-- name: DeleteChannelWebhookEndpoint :execrows
DELETE FROM channel_webhook_endpoint
WHERE channel_id = $1;
