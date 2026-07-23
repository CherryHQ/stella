-- name: CreateChannelWebhookEndpoint :one
INSERT INTO channel_webhook_endpoint (
    id,
    channel_id,
    owner_user_id,
    provider,
    token_public_id,
    token_hash,
    token_last4,
    provider_secret_ciphertext
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetChannelWebhookEndpointByID :one
SELECT * FROM channel_webhook_endpoint
WHERE id = $1;

-- name: GetChannelWebhookEndpointByChannelID :one
SELECT * FROM channel_webhook_endpoint
WHERE channel_id = $1;

-- name: ResolveChannelWebhookEndpointByPublicID :one
SELECT
    endpoint.id AS endpoint_id,
    endpoint.channel_id,
    endpoint.owner_user_id,
    endpoint.provider,
    endpoint.token_hash,
    endpoint.token_last4,
    endpoint.provider_secret_ciphertext,
    endpoint.created_at,
    endpoint.updated_at,
    endpoint.rotated_at,
    channel.agent_id,
    channel.enabled AS channel_enabled,
    auth_user.is_active AS owner_active,
    agent.enabled AS agent_enabled
FROM channel_webhook_endpoint AS endpoint
JOIN channel ON channel.id = endpoint.channel_id
JOIN auth_user ON auth_user.id = endpoint.owner_user_id
JOIN agent ON agent.id = channel.agent_id
WHERE endpoint.token_public_id = $1
  AND channel.type = 'webhook'
  AND channel.enabled = true
  AND auth_user.is_active = true
  AND agent.enabled = true;

-- name: GetWebhookChannelBindingForUpdate :one
SELECT
    channel.id,
    channel.type,
    channel.agent_id,
    COALESCE(agent.enabled, false) AS agent_enabled,
    channel.config
FROM channel
LEFT JOIN agent ON agent.id = channel.agent_id
WHERE channel.id = $1
FOR UPDATE OF channel;

-- name: RotateChannelWebhookEndpoint :one
UPDATE channel_webhook_endpoint
SET token_public_id = $1,
    token_hash = $2,
    token_last4 = $3,
    provider_secret_ciphertext = $4,
    rotated_at = now(),
    updated_at = now()
WHERE id = $5
RETURNING *;

-- name: DeleteChannelWebhookEndpoint :execrows
DELETE FROM channel_webhook_endpoint
WHERE id = $1;
