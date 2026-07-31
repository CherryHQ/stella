-- name: GetChannel :one
SELECT * FROM channel WHERE id = $1;

-- name: CreateChannel :one
INSERT INTO channel (id, name, type, agent_id, enabled, config)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- GetChannelBinding reads the current channel binding without a lock. Endpoint
-- issuance observes it here (outside any transaction) to run owner/access
-- prechecks before taking the short channel-row lock for the final insert.
-- name: GetChannelBinding :one
SELECT
    channel.id,
    channel.type,
    channel.agent_id,
    COALESCE(agent.enabled, false) AS agent_enabled,
    channel.config
FROM channel
LEFT JOIN agent ON agent.id = channel.agent_id
WHERE channel.id = $1;

-- GetChannelBindingForUpdate locks the channel row (and only the channel row)
-- so endpoint issuance and channel binding mutation serialize against each
-- other. The agent join is a read for the caller's binding checks; only the
-- channel row is locked (FOR UPDATE OF channel), so no unrelated row lock is
-- held across the transaction.
-- name: GetChannelBindingForUpdate :one
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

-- name: UpsertChannel :exec
INSERT INTO channel (id, name, type, agent_id, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    type = excluded.type,
    agent_id = excluded.agent_id,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = now();

-- name: CreateWebChannelIfNotExists :exec
INSERT INTO channel (id, name, type, agent_id)
VALUES ($1, 'Web', 'web', $2)
ON CONFLICT(id) DO NOTHING;

-- name: ListChannels :many
SELECT * FROM channel ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM channel WHERE type = $1 ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM channel WHERE id = $1;
