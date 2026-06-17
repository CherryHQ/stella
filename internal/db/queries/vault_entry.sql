-- name: ListVaultEntriesByUser :many
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = 'user' AND user_id = $1
ORDER BY name;

-- name: ListVaultEntriesByScope :many
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id, '') = coalesce(sqlc.narg(user_id), '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
ORDER BY name;

-- name: ListVaultEntriesForRuntime :many
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE (scope = 'system' AND user_id IS NULL AND agent_id IS NULL)
   OR (scope = 'system_agent' AND user_id IS NULL AND agent_id = sqlc.arg(agent_id))
   OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND agent_id IS NULL)
   OR (scope = 'user_agent' AND user_id = sqlc.arg(user_id) AND agent_id = sqlc.arg(agent_id))
-- Keep this precedence in sync with internal/vault envPrecedence.
ORDER BY CASE scope
    WHEN 'system' THEN 1
    WHEN 'system_agent' THEN 2
    WHEN 'user' THEN 3
    WHEN 'user_agent' THEN 4
    ELSE 0
END, name;

-- name: GetVaultEntry :one
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = 'user' AND user_id = $1 AND name = $2;

-- name: GetVaultEntryByScope :one
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id, '') = coalesce(sqlc.narg(user_id), '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: UpsertVaultEntry :exec
INSERT INTO vault_entry (id, scope, user_id, agent_id, name, ciphertext)
VALUES ($1, 'user', $2, NULL, $3, $4)
ON CONFLICT DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = now();

-- name: UpsertVaultEntryByScope :exec
INSERT INTO vault_entry (id, scope, user_id, agent_id, name, ciphertext)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = now();

-- name: DeleteVaultEntry :exec
DELETE FROM vault_entry WHERE scope = 'user' AND user_id = $1 AND name = $2;

-- name: DeleteVaultEntryByScope :exec
DELETE FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id, '') = coalesce(sqlc.narg(user_id), '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: DeleteAllVaultEntriesByUser :exec
DELETE FROM vault_entry WHERE user_id = $1;
