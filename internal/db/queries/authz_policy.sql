-- name: GetAuthzPolicyRevision :one
SELECT revision FROM authz_policy_revision WHERE id = 1;

-- name: BumpAuthzPolicyRevision :one
-- Locks the single revision row and advances it. Called inside a mutation
-- transaction BEFORE the policy write, so the row lock serializes concurrent
-- mutations into commit order. No sequence/nextval.
UPDATE authz_policy_revision
SET revision = revision + 1, updated_at = now()
WHERE id = 1
RETURNING revision;

-- name: ListActiveAuthzPolicy :many
SELECT * FROM authz_policy
WHERE status = 'active'
ORDER BY priority DESC, id;

-- name: ListAuthzPolicy :many
SELECT * FROM authz_policy
ORDER BY priority DESC, id;

-- name: ListQuarantinedAuthzPolicy :many
SELECT * FROM authz_policy
WHERE status = 'quarantined'
ORDER BY id;

-- name: GetAuthzPolicy :one
SELECT * FROM authz_policy WHERE id = $1;

-- name: CreateAuthzPolicy :one
INSERT INTO authz_policy (
    id, name, resource_type, action, effect, subjects, attributes,
    catalog_version, status, quarantine_reason, priority
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: UpdateAuthzPolicy :one
UPDATE authz_policy SET
    name = $2,
    resource_type = $3,
    action = $4,
    effect = $5,
    subjects = $6,
    attributes = $7,
    catalog_version = $8,
    status = $9,
    quarantine_reason = $10,
    priority = $11,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteAuthzPolicy :exec
DELETE FROM authz_policy WHERE id = $1;
