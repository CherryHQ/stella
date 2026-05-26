-- name: GetProvider :one
SELECT * FROM settings_provider WHERE id = ? AND org_id = ?;

-- name: ListProviders :many
SELECT * FROM settings_provider WHERE org_id = ? ORDER BY name, id;

-- name: ListEnabledProviders :many
SELECT * FROM settings_provider WHERE org_id = ? AND enabled = 1 ORDER BY name, id;

-- name: CreateProvider :one
INSERT INTO settings_provider (id, type, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
RETURNING *;

-- name: UpdateProvider :exec
UPDATE settings_provider SET
    type = ?,
    name = ?,
    enabled = ?,
    config = ?,
    updated_at = datetime('now')
WHERE id = ? AND org_id = ?;

-- name: DeleteProvider :exec
DELETE FROM settings_provider WHERE id = ? AND org_id = ?;

-- name: SeedProvider :exec
INSERT OR IGNORE INTO settings_provider (id, type, name, enabled, config, org_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: SetProviderOrg :exec
UPDATE settings_provider SET org_id = ? WHERE id = ?;
