-- name: CreateAuthIdentity :one
INSERT INTO auth_identities (id, user_id, platform, external_id, name)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAuthIdentity :one
SELECT * FROM auth_identities WHERE id = ?;

-- name: GetAuthIdentityByPlatform :one
SELECT * FROM auth_identities WHERE platform = ? AND external_id = ?;

-- name: UpdateAuthIdentityExternalID :exec
UPDATE auth_identities
SET external_id = ?
WHERE id = ?;

-- name: ListAuthIdentitiesByUser :many
SELECT * FROM auth_identities WHERE user_id = ? ORDER BY linked_at;

-- name: DeleteAuthIdentity :exec
DELETE FROM auth_identities WHERE id = ?;
