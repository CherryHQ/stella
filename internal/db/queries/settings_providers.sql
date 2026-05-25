-- name: GetProvider :one
SELECT * FROM settings_providers WHERE id = ?;

-- name: ListProviders :many
SELECT * FROM settings_providers ORDER BY name, id;

-- name: ListEnabledProviders :many
SELECT * FROM settings_providers WHERE enabled = 1 ORDER BY name, id;

-- name: CreateProvider :one
INSERT INTO settings_providers (id, type, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
RETURNING *;

-- name: UpdateProvider :exec
UPDATE settings_providers SET
    type = ?,
    name = ?,
    enabled = ?,
    config = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteProvider :exec
DELETE FROM settings_providers WHERE id = ?;

-- name: SeedProvider :exec
INSERT OR IGNORE INTO settings_providers (id, type, name, enabled, config, org_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListProvidersByOrg :many
SELECT * FROM settings_providers WHERE org_id = ? ORDER BY name, id;

-- name: ListEnabledProvidersByOrg :many
SELECT * FROM settings_providers WHERE org_id = ? AND enabled = 1 ORDER BY name, id;

-- name: SetProviderOrg :exec
UPDATE settings_providers SET org_id = ? WHERE id = ?;
