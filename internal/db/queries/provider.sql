-- name: GetProvider :one
SELECT * FROM provider WHERE id = $1;

-- name: ListProviders :many
SELECT * FROM provider ORDER BY name, id;

-- name: ListProviderIDs :many
-- Credential validation needs canonical IDs, never Provider config or its
-- deployment-global API key.
SELECT id FROM provider ORDER BY id;

-- name: ListEnabledProviders :many
SELECT * FROM provider WHERE enabled = true ORDER BY name, id;

-- name: CreateProvider :one
INSERT INTO provider (id, type, name, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
RETURNING *;

-- name: UpdateProvider :exec
UPDATE provider SET
    type = $1,
    name = $2,
    enabled = $3,
    config = $4,
    updated_at = now()
WHERE id = $5;

-- name: DeleteProvider :exec
DELETE FROM provider WHERE id = $1;

-- name: UpdateProviderIfVersion :execrows
UPDATE provider SET
    type = sqlc.arg(type),
    name = sqlc.arg(name),
    enabled = sqlc.arg(enabled),
    config = sqlc.arg(config),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND updated_at = sqlc.arg(expected_updated_at);

-- name: DeleteProviderIfVersion :execrows
DELETE FROM provider
WHERE id = sqlc.arg(id)
  AND updated_at = sqlc.arg(expected_updated_at);
