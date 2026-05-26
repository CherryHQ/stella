-- name: GetChannel :one
SELECT * FROM settings_channel WHERE id = ?;

-- name: UpsertChannel :exec
INSERT INTO settings_channel (id, type, agent_id, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    type = excluded.type,
    agent_id = excluded.agent_id,
    enabled = excluded.enabled,
    config = excluded.config,
    org_id = excluded.org_id,
    updated_at = datetime('now');

-- name: ListChannels :many
SELECT * FROM settings_channel WHERE org_id = ? ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM settings_channel WHERE type = ? AND org_id = ? ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM settings_channel WHERE id = ?;

-- name: SetChannelOrg :exec
UPDATE settings_channel SET org_id = ? WHERE id = ?;
