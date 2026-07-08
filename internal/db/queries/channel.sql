-- name: GetChannel :one
SELECT * FROM channel WHERE id = $1;

-- name: UpsertChannel :exec
INSERT INTO channel (id, name, type, agent_id, user_id, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    type = excluded.type,
    agent_id = excluded.agent_id,
    user_id = excluded.user_id,
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
