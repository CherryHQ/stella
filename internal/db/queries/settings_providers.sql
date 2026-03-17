-- name: CreateProvider :one
INSERT INTO settings_providers (id, name, api_key, base_url)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetProvider :one
SELECT * FROM settings_providers WHERE id = ?;

-- name: ListProviders :many
SELECT * FROM settings_providers ORDER BY name;

-- name: UpdateProvider :exec
UPDATE settings_providers SET
    name = ?,
    api_key = ?,
    base_url = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteProvider :exec
DELETE FROM settings_providers WHERE id = ?;
