-- Migration-only child identities for legacy authored MCP entries.

-- name: CreatePluginConfigMCPServer :one
INSERT INTO plugin_config_mcp_server (id, config_id, server_key, updated_at)
VALUES ($1, $2, $3, now())
RETURNING *;

-- name: ListPluginConfigMCPServersForConfig :many
SELECT * FROM plugin_config_mcp_server
WHERE config_id = $1
ORDER BY server_key;
