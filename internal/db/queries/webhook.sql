-- name: CreateWebhook :one
INSERT INTO webhook (
    id, user_id, agent_id, name, provider, is_enabled,
    wait_timeout_seconds, max_run_timeout_seconds,
    token_public_id, token_hash, token_last4
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetWebhookForUser :one
SELECT * FROM webhook WHERE id = $1 AND user_id = $2;

-- name: GetWebhookForUserForUpdate :one
SELECT * FROM webhook WHERE id = $1 AND user_id = $2 FOR UPDATE;

-- name: ListWebhookForUser :many
SELECT * FROM webhook
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: UpdateWebhookForUser :one
UPDATE webhook
SET name = $3,
    agent_id = $4,
    is_enabled = $5,
    wait_timeout_seconds = $6,
    max_run_timeout_seconds = $7,
    updated_at = now()
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: GetWebhookByPublicID :one
SELECT * FROM webhook WHERE token_public_id = $1;

-- name: ResolveWebhookByPublicID :one
SELECT
    webhook.id,
    webhook.user_id,
    webhook.agent_id,
    webhook.name,
    webhook.provider,
    webhook.is_enabled,
    webhook.wait_timeout_seconds,
    webhook.max_run_timeout_seconds,
    webhook.token_public_id,
    webhook.token_hash,
    webhook.token_last4,
    webhook.revision,
    webhook.created_at,
    webhook.updated_at,
    webhook.rotated_at
FROM webhook
JOIN auth_user ON auth_user.id = webhook.user_id
JOIN agent ON agent.id = webhook.agent_id
WHERE webhook.token_public_id = $1
  AND webhook.is_enabled = true
  AND auth_user.is_active = true
  AND agent.enabled = true;

-- name: RotateWebhook :one
UPDATE webhook
SET token_public_id = $3,
    token_hash = $4,
    token_last4 = $5,
    revision = revision + 1,
    rotated_at = now(),
    updated_at = now()
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteWebhookForUser :execrows
DELETE FROM webhook WHERE id = $1 AND user_id = $2;
