-- name: GetChannel :one
SELECT * FROM channel WHERE id = ?;

-- name: UpsertChannel :exec
INSERT INTO channel (id, name, type, agent_id, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    name = excluded.name,
    type = excluded.type,
    agent_id = excluded.agent_id,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListChannels :many
SELECT * FROM channel ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM channel WHERE type = ? ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM channel WHERE id = ?;
