-- name: GetChannel :one
SELECT * FROM settings_channels WHERE id = ?;

-- name: UpsertChannel :exec
INSERT INTO settings_channels (id, type, agent_id, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    type = excluded.type,
    agent_id = excluded.agent_id,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListChannels :many
SELECT * FROM settings_channels ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM settings_channels WHERE type = ? ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM settings_channels WHERE id = ?;

-- name: ListChannelsByOrg :many
SELECT * FROM settings_channels WHERE org_id = ? ORDER BY type, id;

-- name: ListChannelsByTypeAndOrg :many
SELECT * FROM settings_channels WHERE type = ? AND org_id = ? ORDER BY id;

-- name: SetChannelOrg :exec
UPDATE settings_channels SET org_id = ? WHERE id = ?;
