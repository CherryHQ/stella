-- name: CreateChannelWebhookEndpoint :one
INSERT INTO channel_webhook_endpoint (
    channel_id,
    provider,
    token_public_id,
    token_hash,
    token_last4
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetChannelWebhookEndpointByChannelID :one
SELECT * FROM channel_webhook_endpoint
WHERE channel_id = $1;

-- Endpoint metadata always derives ownership from its channel. The endpoint
-- table intentionally has no second owner column.
-- name: GetChannelWebhookEndpointByChannelIDForOwner :one
SELECT
    endpoint.channel_id,
    channel.owner_user_id,
    endpoint.provider,
    endpoint.token_public_id,
    endpoint.token_hash,
    endpoint.token_last4,
    endpoint.revision,
    endpoint.created_at,
    endpoint.updated_at,
    endpoint.rotated_at
FROM channel_webhook_endpoint AS endpoint
JOIN channel ON channel.id = endpoint.channel_id
WHERE endpoint.channel_id = $1
  AND channel.type = 'webhook'
  AND channel.owner_user_id = $2;

-- name: GetChannelWebhookEndpointByChannelIDForOwnerForUpdate :one
SELECT
    endpoint.channel_id,
    channel.owner_user_id,
    endpoint.provider,
    endpoint.token_public_id,
    endpoint.token_hash,
    endpoint.token_last4,
    endpoint.revision,
    endpoint.created_at,
    endpoint.updated_at,
    endpoint.rotated_at
FROM channel_webhook_endpoint AS endpoint
JOIN channel ON channel.id = endpoint.channel_id
WHERE endpoint.channel_id = $1
  AND channel.type = 'webhook'
  AND channel.owner_user_id = $2
FOR UPDATE OF endpoint;

-- name: GetChannelWebhookEndpointByPublicID :one
SELECT * FROM channel_webhook_endpoint
WHERE token_public_id = $1;

-- ResolveChannelWebhookEndpointByPublicID is the deep admission read: it joins
-- the channel owner and Agent and returns a row only while all three are active.
-- Any disabled/inactive party (or a rotated/revoked credential) yields no row,
-- so admission fails closed. The owner→Agent permission (PEP) is checked
-- separately in Go; this only confirms durable active state.
-- name: ResolveChannelWebhookEndpointByPublicID :one
SELECT
    endpoint.channel_id,
    channel.owner_user_id,
    endpoint.provider,
    endpoint.token_hash,
    endpoint.token_last4,
    endpoint.revision,
    endpoint.created_at,
    endpoint.updated_at,
    endpoint.rotated_at,
    channel.agent_id,
    channel.enabled AS channel_enabled,
    auth_user.is_active AS owner_active,
    agent.enabled AS agent_enabled
FROM channel_webhook_endpoint AS endpoint
JOIN channel ON channel.id = endpoint.channel_id
JOIN auth_user ON auth_user.id = channel.owner_user_id
JOIN agent ON agent.id = channel.agent_id
WHERE endpoint.token_public_id = $1
  AND channel.type = 'webhook'
  AND channel.enabled = true
  AND auth_user.is_active = true
  AND agent.enabled = true;

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

-- name: DeleteChannelWebhookEndpointForOwner :execrows
DELETE FROM channel_webhook_endpoint AS endpoint
USING channel
WHERE endpoint.channel_id = channel.id
  AND endpoint.channel_id = $1
  AND channel.type = 'webhook'
  AND channel.owner_user_id = $2;
