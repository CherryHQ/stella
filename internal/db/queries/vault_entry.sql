-- name: ListVaultEntriesByUser :many
SELECT *
FROM vault_entry
WHERE scope = 'user' AND user_id = $1
ORDER BY name;

-- name: ListVaultEntriesByScope :many
SELECT *
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
ORDER BY name;

-- name: ListVaultEntriesForRuntime :many
SELECT *
FROM vault_entry
WHERE ((scope = 'system' AND user_id IS NULL AND vault_entry.agent_id IS NULL)
    OR (scope = 'system_agent' AND user_id IS NULL AND vault_entry.agent_id = sqlc.arg(runtime_agent_id))
    OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id IS NULL)
    OR (scope = 'user_agent' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id = sqlc.arg(runtime_agent_id)))
  AND (inject_always
    OR EXISTS (
        SELECT 1
        FROM vault_entry_agent_binding b
        WHERE b.vault_entry_id = vault_entry.id
          AND b.agent_id = sqlc.arg(runtime_agent_id)
    )
    OR EXISTS (
        SELECT 1
        FROM vault_entry_project_binding b
        WHERE b.vault_entry_id = vault_entry.id
          AND b.project_id = sqlc.narg(project_id)::uuid
    ))
-- Keep this precedence in sync with internal/vault envPrecedence.
ORDER BY CASE scope
    WHEN 'system' THEN 1
    WHEN 'system_agent' THEN 2
    WHEN 'user' THEN 3
    WHEN 'user_agent' THEN 4
    ELSE 0
END, name;

-- name: ListVaultEntriesForRuntimeFull :many
SELECT *
FROM vault_entry
WHERE (scope = 'system' AND user_id IS NULL AND vault_entry.agent_id IS NULL)
   OR (scope = 'system_agent' AND user_id IS NULL AND vault_entry.agent_id = sqlc.arg(runtime_agent_id))
   OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id IS NULL)
   OR (scope = 'user_agent' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id = sqlc.arg(runtime_agent_id))
-- Keep this precedence in sync with internal/vault envPrecedence.
ORDER BY CASE scope
    WHEN 'system' THEN 1
    WHEN 'system_agent' THEN 2
    WHEN 'user' THEN 3
    WHEN 'user_agent' THEN 4
    ELSE 0
END, name;

-- name: ListVaultEntriesDeclarableForRuntime :many
SELECT *
FROM vault_entry
WHERE ((scope = 'system' AND user_id IS NULL AND vault_entry.agent_id IS NULL)
    OR (scope = 'system_agent' AND user_id IS NULL AND vault_entry.agent_id = sqlc.arg(runtime_agent_id))
    OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id IS NULL)
    OR (scope = 'user_agent' AND user_id = sqlc.arg(user_id) AND vault_entry.agent_id = sqlc.arg(runtime_agent_id)))
  AND NOT (inject_always
    OR EXISTS (
        SELECT 1
        FROM vault_entry_agent_binding b
        WHERE b.vault_entry_id = vault_entry.id
          AND b.agent_id = sqlc.arg(runtime_agent_id)
    )
    OR EXISTS (
        SELECT 1
        FROM vault_entry_project_binding b
        WHERE b.vault_entry_id = vault_entry.id
          AND b.project_id = sqlc.narg(project_id)::uuid
    ))
ORDER BY CASE scope
    WHEN 'system' THEN 1
    WHEN 'system_agent' THEN 2
    WHEN 'user' THEN 3
    WHEN 'user_agent' THEN 4
    ELSE 0
END, name;

-- name: GetVaultEntry :one
SELECT *
FROM vault_entry
WHERE scope = 'user' AND user_id = $1 AND name = $2;

-- name: GetVaultEntryByScope :one
SELECT *
FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: UpsertVaultEntryByScope :one
INSERT INTO vault_entry (id, scope, user_id, agent_id, name, ciphertext, inject_always, description)
VALUES ($1, $2, $3, $4, $5, $6, coalesce(sqlc.narg(inject_always), false), sqlc.narg(description))
ON CONFLICT (scope, (COALESCE(user_id::text, '')), (COALESCE(agent_id, '')), name) DO UPDATE SET
    ciphertext = excluded.ciphertext,
    inject_always = coalesce(sqlc.narg(inject_always), vault_entry.inject_always),
    description = coalesce(sqlc.narg(description), vault_entry.description),
    updated_at = now()
RETURNING *;

-- name: DeleteVaultEntry :exec
DELETE FROM vault_entry WHERE scope = 'user' AND user_id = $1 AND name = $2;

-- name: DeleteVaultEntryByScope :exec
DELETE FROM vault_entry
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND name = sqlc.arg(name);

-- name: DeleteAllVaultEntriesByUser :exec
DELETE FROM vault_entry WHERE user_id = $1;

-- name: ListVaultEntryAgentBindings :many
SELECT agent_id
FROM vault_entry_agent_binding
WHERE vault_entry_id = $1
ORDER BY agent_id;

-- name: ReplaceVaultEntryAgentBindings :exec
WITH deleted AS (
    DELETE FROM vault_entry_agent_binding
    WHERE vault_entry_id = sqlc.arg(vault_entry_id)
)
INSERT INTO vault_entry_agent_binding (vault_entry_id, agent_id)
SELECT sqlc.arg(vault_entry_id), unnest(sqlc.arg(agent_ids)::text[]);

-- name: ListVaultEntryProjectBindings :many
SELECT project_id
FROM vault_entry_project_binding
WHERE vault_entry_id = $1
ORDER BY project_id;

-- name: ReplaceVaultEntryProjectBindings :exec
WITH deleted AS (
    DELETE FROM vault_entry_project_binding
    WHERE vault_entry_id = sqlc.arg(vault_entry_id)
)
INSERT INTO vault_entry_project_binding (vault_entry_id, project_id)
SELECT sqlc.arg(vault_entry_id), unnest(sqlc.arg(project_ids)::uuid[]);
