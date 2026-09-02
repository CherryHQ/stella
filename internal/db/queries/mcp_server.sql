-- name: CreateMCPServer :one
INSERT INTO mcp_server (id, scope, user_id, agent_id, name, url, transport, auth_type, credential_ref, enabled, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetMCPServerByID :one
SELECT * FROM mcp_server WHERE id = $1;

-- ListMCPServersByScope returns every registration in exactly one scope/owner
-- bucket, for management (list/delete) operations.
-- name: ListMCPServersByScope :many
SELECT * FROM mcp_server
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
ORDER BY name;

-- ListMCPServersForAgentContext returns the visible, enabled registrations for
-- one (user, agent), ordered most-specific-first so a name-dedup downstream
-- keeps the effective server: user_agent > user > system_agent > system.
-- name: ListMCPServersForAgentContext :many
SELECT * FROM mcp_server
WHERE enabled = true
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id))
    OR (scope = 'user'         AND user_id = sqlc.narg(user_id))
    OR (scope = 'user_agent'   AND user_id = sqlc.narg(user_id) AND agent_id = sqlc.narg(agent_id))
  )
ORDER BY CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END, name;

-- name: UpdateMCPServerByScope :one
UPDATE mcp_server
SET scope = sqlc.arg(new_scope),
    user_id = sqlc.narg(new_user_id),
    agent_id = sqlc.narg(new_agent_id),
    name = sqlc.arg(name),
    url = sqlc.arg(url),
    transport = sqlc.arg(transport),
    auth_type = sqlc.arg(auth_type),
    credential_ref = sqlc.arg(credential_ref),
    enabled = sqlc.arg(enabled),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
RETURNING *;

-- name: UpdateMCPServerByScopeIfVersion :one
UPDATE mcp_server
SET scope = sqlc.arg(new_scope),
    user_id = sqlc.narg(new_user_id),
    agent_id = sqlc.narg(new_agent_id),
    name = sqlc.arg(name),
    url = sqlc.arg(url),
    transport = sqlc.arg(transport),
    auth_type = sqlc.arg(auth_type),
    credential_ref = sqlc.arg(credential_ref),
    enabled = sqlc.arg(enabled),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND updated_at = sqlc.arg(expected_updated_at)
RETURNING *;

-- name: DeleteMCPServerByScope :exec
DELETE FROM mcp_server
WHERE id = sqlc.arg(id)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '');

-- name: DeleteMCPServerByScopeIfVersion :execrows
DELETE FROM mcp_server
WHERE id = sqlc.arg(id)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
  AND updated_at = sqlc.arg(expected_updated_at);

-- Probe writes must NOT set updated_at: a probe is an observation, not a
-- user edit, and the opaque Version() used for If-Match must not change when
-- only the probe result changed.
-- name: UpdateMCPServerProbeResult :one
UPDATE mcp_server
SET status = sqlc.arg(status),
    status_error = sqlc.arg(status_error),
    probed_at = sqlc.arg(probed_at),
    tools = sqlc.arg(tools)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateMCPServerStatus :exec
UPDATE mcp_server
SET status = sqlc.arg(status), status_error = sqlc.arg(status_error)
WHERE id = sqlc.arg(id);

-- name: UpdateMCPServerEnabled :exec
UPDATE mcp_server
SET enabled = sqlc.arg(enabled), updated_at = now()
WHERE id = sqlc.arg(id)
  AND scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '');
