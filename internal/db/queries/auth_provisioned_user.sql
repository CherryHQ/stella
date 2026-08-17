-- name: CreateProvisionedAuthUser :one
INSERT INTO auth_user (
    id, email, name, role, is_active, age_public_key, age_private_key
) VALUES (
    sqlc.arg(id), sqlc.arg(email), sqlc.arg(name), 'user', true,
    sqlc.arg(age_public_key), sqlc.arg(age_private_key)
)
RETURNING *;

-- name: CreateProvisionedUser :one
INSERT INTO auth_provisioned_user (
    id, external_id, user_id, created_by_user_id, created_by_token_id
) VALUES (
    sqlc.arg(id), sqlc.arg(external_id), sqlc.arg(user_id),
    sqlc.narg(created_by_user_id), sqlc.narg(created_by_token_id)
)
RETURNING *;

-- name: CreateProvisionedPersonalAccessToken :one
INSERT INTO personal_access_token (
    public_id, user_id, name, token_hash, last4, scopes, expires_at,
    token_use, issued_by_token_id, issued_by_provisioning
) VALUES (
    sqlc.arg(public_id), sqlc.arg(user_id), sqlc.arg(name),
    sqlc.arg(token_hash), sqlc.arg(last4), sqlc.arg(scopes),
    sqlc.arg(expires_at), 'personal', sqlc.arg(issued_by_token_id), true
)
RETURNING *;

-- name: GetProvisionedUser :one
SELECT
    pu.id, pu.external_id, pu.user_id, pu.created_by_user_id,
    pu.created_by_token_id, pu.created_at,
    GREATEST(pu.updated_at, u.updated_at, COALESCE(p.updated_at, 'epoch'::timestamptz))::timestamptz AS updated_at,
    u.email, u.name, u.role, u.is_active,
    CASE WHEN p.id IS NULL THEN ''::text ELSE p.id::text END AS token_id,
    COALESCE(p.name, '') AS token_name,
    COALESCE(p.last4, '') AS token_last4,
    p.expires_at AS token_expires_at, p.last_used_at AS token_last_used_at,
    COALESCE(p.created_at, 'epoch'::timestamptz) AS token_created_at
FROM auth_provisioned_user pu
JOIN auth_user u ON u.id = pu.user_id
LEFT JOIN LATERAL (
    SELECT id, name, last4, expires_at, last_used_at, created_at, updated_at
    FROM personal_access_token
    WHERE user_id = pu.user_id
      AND issued_by_provisioning
      AND revoked_at IS NULL
      AND (expires_at IS NULL OR expires_at > now())
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) p ON true
WHERE pu.id = sqlc.arg(id);

-- name: GetProvisionedUserByExternalID :one
SELECT
    pu.id, pu.external_id, pu.user_id, pu.created_by_user_id,
    pu.created_by_token_id, pu.created_at,
    GREATEST(pu.updated_at, u.updated_at, COALESCE(p.updated_at, 'epoch'::timestamptz))::timestamptz AS updated_at,
    u.email, u.name, u.role, u.is_active,
    CASE WHEN p.id IS NULL THEN ''::text ELSE p.id::text END AS token_id,
    COALESCE(p.name, '') AS token_name,
    COALESCE(p.last4, '') AS token_last4,
    p.expires_at AS token_expires_at, p.last_used_at AS token_last_used_at,
    COALESCE(p.created_at, 'epoch'::timestamptz) AS token_created_at
FROM auth_provisioned_user pu
JOIN auth_user u ON u.id = pu.user_id
LEFT JOIN LATERAL (
    SELECT id, name, last4, expires_at, last_used_at, created_at, updated_at
    FROM personal_access_token
    WHERE user_id = pu.user_id
      AND issued_by_provisioning
      AND revoked_at IS NULL
      AND (expires_at IS NULL OR expires_at > now())
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) p ON true
WHERE pu.external_id = sqlc.arg(external_id);

-- name: ListProvisionedUserAfter :many
SELECT
    pu.id, pu.external_id, pu.user_id, pu.created_by_user_id,
    pu.created_by_token_id, pu.created_at,
    GREATEST(pu.updated_at, u.updated_at, COALESCE(p.updated_at, 'epoch'::timestamptz))::timestamptz AS updated_at,
    u.email, u.name, u.role, u.is_active,
    CASE WHEN p.id IS NULL THEN ''::text ELSE p.id::text END AS token_id,
    COALESCE(p.name, '') AS token_name,
    COALESCE(p.last4, '') AS token_last4,
    p.expires_at AS token_expires_at, p.last_used_at AS token_last_used_at,
    COALESCE(p.created_at, 'epoch'::timestamptz) AS token_created_at
FROM auth_provisioned_user pu
JOIN auth_user u ON u.id = pu.user_id
LEFT JOIN LATERAL (
    SELECT id, name, last4, expires_at, last_used_at, created_at, updated_at
    FROM personal_access_token
    WHERE user_id = pu.user_id
      AND issued_by_provisioning
      AND revoked_at IS NULL
      AND (expires_at IS NULL OR expires_at > now())
    ORDER BY created_at DESC, id DESC
    LIMIT 1
) p ON true
WHERE (sqlc.narg(cursor_created_at)::timestamptz IS NULL OR (pu.created_at, pu.id) > (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY pu.created_at ASC, pu.id ASC
LIMIT sqlc.arg(page_limit);

-- name: GetProvisionedUserForUpdate :one
SELECT pu.id, pu.external_id, pu.user_id, u.email, u.name, u.role, u.is_active,
       pu.created_at, pu.updated_at
FROM auth_provisioned_user pu
JOIN auth_user u ON u.id = pu.user_id
WHERE pu.id = sqlc.arg(id)
FOR UPDATE OF pu, u;

-- name: GetOwnedProvisionedUserForUpdate :one
SELECT pu.id, pu.user_id, u.role, u.is_active
FROM auth_provisioned_user pu
JOIN auth_user u ON u.id = pu.user_id
WHERE pu.id = sqlc.arg(id)
  AND pu.created_by_user_id = sqlc.arg(created_by_user_id)
FOR UPDATE OF pu, u;

-- name: CreateProvisionedUserChannelIdentity :one
INSERT INTO channel_identity (id, user_id, platform, external_id, name)
VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(platform),
    sqlc.arg(external_id), sqlc.arg(name)
)
RETURNING *;

-- name: RevokeProvisionedPersonalAccessTokenByUser :execrows
UPDATE personal_access_token
SET revoked_at = now(), updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND issued_by_provisioning
  AND revoked_at IS NULL;
