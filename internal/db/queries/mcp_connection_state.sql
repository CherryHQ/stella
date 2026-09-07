-- name: ListMCPConnectionStatesForConfigs :many
SELECT id, child_id, credential_user_id, tools, status, status_error,
       probed_at, config_revision, created_at, updated_at
FROM mcp_connection_state
WHERE child_id = ANY(sqlc.arg(child_ids)::uuid[])
  AND (
      credential_user_id IS NULL
      OR credential_user_id = sqlc.narg(credential_user_id)::uuid
  )
ORDER BY array_position(sqlc.arg(child_ids)::uuid[], child_id), credential_user_id NULLS FIRST, id;

-- name: LockMCPConfigRevision :one
SELECT c.revision
FROM plugin_config_mcp_server child
JOIN plugin_config c ON c.id = child.config_id
WHERE child.id = sqlc.arg(child_id)::uuid
FOR UPDATE OF c;

-- name: UpsertMCPConnectionState :one
INSERT INTO mcp_connection_state (
    child_id, credential_user_id, tools, status, status_error,
    probed_at, config_revision
)
VALUES (
    sqlc.arg(child_id)::uuid,
    sqlc.narg(credential_user_id)::uuid,
    sqlc.arg(tools)::jsonb,
    sqlc.arg(status),
    sqlc.arg(status_error),
    sqlc.narg(probed_at),
    sqlc.arg(config_revision)
)
ON CONFLICT (child_id, credential_user_id) DO UPDATE
SET tools = EXCLUDED.tools,
    status = EXCLUDED.status,
    status_error = EXCLUDED.status_error,
    probed_at = EXCLUDED.probed_at,
    config_revision = EXCLUDED.config_revision,
    updated_at = now()
RETURNING id, child_id, credential_user_id, tools, status, status_error,
          probed_at, config_revision, created_at, updated_at;
