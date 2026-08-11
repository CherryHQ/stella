-- name: CreateStorageHome :one
INSERT INTO storage_home (
    id, home_kind, principal_kind, principal_id, agent_id, store_id, locator
) VALUES (
    sqlc.arg(id), sqlc.arg(home_kind), sqlc.narg(principal_kind),
    sqlc.narg(principal_id), sqlc.narg(agent_id), sqlc.arg(store_id), sqlc.arg(locator)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetStorageHome :one
SELECT * FROM storage_home WHERE id = $1;

-- name: LockStorageHomeOwner :exec
SELECT pg_advisory_xact_lock(hashtextextended($1, 0));

-- name: ListStorageHomeStoreID :many
SELECT DISTINCT store_id FROM storage_home ORDER BY store_id;

-- name: ListStorageLegacyUserID :many
SELECT id FROM auth_user ORDER BY id;

-- name: ListStorageLegacyGroupID :many
SELECT id FROM ctx_group_state ORDER BY id;

-- name: ListStorageLegacyAgentID :many
SELECT id FROM agent ORDER BY id;

-- name: ListStorageLegacyUserAgent :many
SELECT user_id, agent_id FROM auth_user_agent ORDER BY user_id, agent_id;

-- name: ListStorageLegacyGroupAgent :many
SELECT group_id, agent_id FROM channel_group_member ORDER BY group_id, agent_id;

-- name: GetPrincipalStorageHome :one
SELECT * FROM storage_home
WHERE home_kind = 'principal' AND principal_kind = $1 AND principal_id = $2;

-- name: GetAgentStorageHome :one
SELECT * FROM storage_home
WHERE home_kind = 'agent' AND principal_kind = $1 AND principal_id = $2 AND agent_id = $3;

-- name: GetSystemSkillStorageHome :one
SELECT * FROM storage_home WHERE home_kind = 'system_skill';

-- name: GetSystemAgentSkillStorageHome :one
SELECT * FROM storage_home WHERE home_kind = 'system_agent_skill' AND agent_id = $1;

-- name: ListStorageHomeByPrincipalForUpdate :many
SELECT * FROM storage_home
WHERE principal_kind = $1 AND principal_id = $2
ORDER BY CASE WHEN home_kind = 'agent' THEN 0 ELSE 1 END, id
FOR UPDATE;

-- name: ListStorageHomeByAgentForUpdate :many
SELECT * FROM storage_home
WHERE agent_id = $1 AND home_kind IN ('agent', 'system_agent_skill')
ORDER BY CASE WHEN home_kind = 'agent' THEN 0 ELSE 1 END, id
FOR UPDATE;

-- name: MarkStorageHomeReady :one
UPDATE storage_home SET state = 'ready', updated_at = now()
WHERE id = $1 AND state = 'provisioning'
RETURNING *;

-- name: TombstoneStorageHome :one
UPDATE storage_home
SET state = 'tombstoned', tombstoned_at = now(), tombstoned_by = $2, updated_at = now()
WHERE id = $1 AND state IN ('provisioning', 'ready')
RETURNING *;

-- name: GetStorageMigration :one
SELECT * FROM storage_migration WHERE name = $1;

-- name: UpsertStorageMigrationObservation :one
INSERT INTO storage_migration (name, state, object_authority_configured, metadata)
VALUES ($1, $2, $3, $4)
ON CONFLICT (name) DO UPDATE
SET state = CASE
        WHEN storage_migration.object_authority_configured OR excluded.object_authority_configured THEN 'pending'
        ELSE excluded.state
    END,
    object_authority_configured = storage_migration.object_authority_configured OR excluded.object_authority_configured,
    metadata = excluded.metadata,
    updated_at = now()
WHERE storage_migration.state <> 'completed'
RETURNING *;

-- name: CreateStorageMigration :one
INSERT INTO storage_migration (name, state, metadata)
VALUES ($1, 'pending', $2)
ON CONFLICT (name) DO NOTHING
RETURNING *;

-- name: CompleteStorageMigration :one
UPDATE storage_migration SET state = 'completed', completed_at = now(), updated_at = now()
WHERE name = $1 AND state = $2
RETURNING *;
