-- name: GetProvider :one
SELECT * FROM settings_provider WHERE id = ?;

-- name: ListProviders :many
SELECT * FROM settings_provider ORDER BY name, id;

-- name: ListEnabledProviders :many
SELECT * FROM settings_provider WHERE enabled = 1 ORDER BY name, id;

-- name: CreateProvider :one
INSERT INTO settings_provider (id, type, name, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
RETURNING *;

-- name: UpdateProvider :exec
UPDATE settings_provider SET
    type = ?,
    name = ?,
    enabled = ?,
    config = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteProvider :exec
DELETE FROM settings_provider WHERE id = ?;

-- name: SeedProvider :exec
INSERT OR IGNORE INTO settings_provider (id, type, name, enabled, config)
VALUES (?, ?, ?, ?, ?);
