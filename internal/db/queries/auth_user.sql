-- name: ListAuthUsersByIDs :many
SELECT * FROM auth_user WHERE id = ANY(sqlc.arg('ids')::text[]) ORDER BY id;

-- name: UpdateUserRole :exec
UPDATE auth_user SET role = $1, updated_at = now() WHERE id = $2;

-- name: UpdateUserActive :exec
UPDATE auth_user SET is_active = $1, updated_at = now() WHERE id = $2;
