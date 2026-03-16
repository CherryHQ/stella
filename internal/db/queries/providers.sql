-- name: CreateProvider :one
INSERT INTO providers (id, name, api_key, base_url)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetProvider :one
SELECT * FROM providers WHERE id = ?;

-- name: ListProviders :many
SELECT * FROM providers ORDER BY name;

-- name: UpdateProvider :exec
UPDATE providers SET
    name = ?,
    api_key = ?,
    base_url = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteProvider :exec
DELETE FROM providers WHERE id = ?;
