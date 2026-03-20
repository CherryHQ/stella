-- name: AssignUserRole :exec
INSERT INTO auth_user_roles (user_id, role_id)
VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: RemoveUserRole :exec
DELETE FROM auth_user_roles WHERE user_id = ? AND role_id = ?;

-- name: ListUserRoles :many
SELECT r.* FROM auth_roles r
JOIN auth_user_roles ur ON ur.role_id = r.id
WHERE ur.user_id = ?
ORDER BY r.name;

-- name: ListRoleUsers :many
SELECT u.* FROM auth_users u
JOIN auth_user_roles ur ON ur.user_id = u.id
WHERE ur.role_id = ?
ORDER BY u.username;
