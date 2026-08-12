-- name: ListAuthUsersByIDs :many
SELECT * FROM auth_user WHERE id = ANY(sqlc.arg('ids')::uuid[]) ORDER BY id;

-- name: GetAuthUser :one
SELECT * FROM auth_user WHERE id = $1;

-- name: GetAuthUserForUpdate :one
SELECT * FROM auth_user WHERE id = $1 FOR UPDATE;

-- name: DeleteAuthUser :exec
DELETE FROM auth_user WHERE id = $1;

-- name: UpdateUserRole :exec
UPDATE auth_user SET role = $1, updated_at = now() WHERE id = $2;

-- name: UpdateUserActive :exec
UPDATE auth_user SET is_active = $1, updated_at = now() WHERE id = $2;
