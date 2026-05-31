-- name: GetChannel :one
SELECT * FROM settings_channel WHERE id = ?;

-- name: UpsertChannel :exec
INSERT INTO settings_channel (id, type, agent_id, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    type = excluded.type,
    agent_id = excluded.agent_id,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListChannels :many
SELECT * FROM settings_channel ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM settings_channel WHERE type = ? ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM settings_channel WHERE id = ?;
