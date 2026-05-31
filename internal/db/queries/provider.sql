-- name: GetProvider :one
SELECT * FROM provider WHERE id = ?;

-- name: ListProviders :many
SELECT * FROM provider ORDER BY name, id;

-- name: ListEnabledProviders :many
SELECT * FROM provider WHERE enabled = 1 ORDER BY name, id;

-- name: CreateProvider :one
INSERT INTO provider (id, type, name, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
RETURNING *;

-- name: UpdateProvider :exec
UPDATE provider SET
    type = ?,
    name = ?,
    enabled = ?,
    config = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteProvider :exec
DELETE FROM provider WHERE id = ?;

-- name: SeedProvider :exec
INSERT OR IGNORE INTO provider (id, type, name, enabled, config)
VALUES (?, ?, ?, ?, ?);
