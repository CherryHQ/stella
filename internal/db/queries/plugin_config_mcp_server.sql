-- Stable child identities for authored MCP server entries.

-- name: CreatePluginConfigMCPServer :one
INSERT INTO plugin_config_mcp_server (id, config_id, server_key, updated_at)
VALUES ($1, $2, $3, now())
RETURNING *;

-- name: ListPluginConfigMCPServers :many
SELECT * FROM plugin_config_mcp_server
WHERE config_id = ANY(sqlc.arg(config_ids)::uuid[])
ORDER BY array_position(sqlc.arg(config_ids)::uuid[], config_id), server_key;

-- name: ListPluginConfigMCPServersForConfig :many
SELECT * FROM plugin_config_mcp_server
WHERE config_id = $1
ORDER BY server_key;

-- name: DeletePluginConfigMCPServers :exec
DELETE FROM plugin_config_mcp_server WHERE config_id = $1;

-- name: DeletePluginConfigMCPServer :exec
DELETE FROM plugin_config_mcp_server WHERE config_id = $1 AND id = $2;

-- name: DeletePluginConfigMCPServersExceptKeys :exec
DELETE FROM plugin_config_mcp_server
WHERE config_id = $1 AND NOT (server_key = ANY(sqlc.arg(server_keys)::text[]));
