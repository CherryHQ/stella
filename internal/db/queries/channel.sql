-- name: GetChannel :one
SELECT * FROM channel WHERE id = $1;

-- name: GetChannelForUpdate :one
SELECT * FROM channel WHERE id = $1 FOR UPDATE;

-- name: CreateChannel :one
INSERT INTO channel (id, name, type, agent_id, enabled, config)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateChannel :one
UPDATE channel
SET name = $2,
    type = $3,
    agent_id = $4,
    enabled = $5,
    config = $6,
    updated_at = now()
WHERE id = $1
RETURNING *;

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
