-- name: ListVaultEntriesByUser :many
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = 'user' AND user_id = ?
ORDER BY name;

-- name: ListVaultEntriesByScope :many
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND ifnull(user_id, '') = ifnull(sqlc.narg(user_id), '')
  AND ifnull(agent_id, '') = ifnull(sqlc.narg(agent_id), '')
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
WHERE scope = 'user' AND user_id = ? AND name = ?;

-- name: GetVaultEntryByScope :one
SELECT id, scope, user_id, agent_id, name, ciphertext, created_at, updated_at
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND ifnull(user_id, '') = ifnull(sqlc.narg(user_id), '')
  AND ifnull(agent_id, '') = ifnull(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: UpsertVaultEntry :exec
INSERT INTO vault_entry (id, scope, user_id, agent_id, name, ciphertext)
VALUES (?, 'user', ?, NULL, ?, ?)
ON CONFLICT DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = datetime('now');

-- name: UpsertVaultEntryByScope :exec
INSERT INTO vault_entry (id, scope, user_id, agent_id, name, ciphertext)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT DO UPDATE SET
    ciphertext = excluded.ciphertext,
    updated_at = datetime('now');

-- name: DeleteVaultEntry :exec
DELETE FROM vault_entry WHERE scope = 'user' AND user_id = ? AND name = ?;

-- name: DeleteVaultEntryByScope :exec
DELETE FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND ifnull(user_id, '') = ifnull(sqlc.narg(user_id), '')
  AND ifnull(agent_id, '') = ifnull(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: DeleteAllVaultEntriesByUser :exec
DELETE FROM vault_entry WHERE user_id = ?;
