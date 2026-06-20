-- name: GetPluginStateEntry :one
SELECT value FROM plugin_state
WHERE plugin_id = $1 AND scope_kind = $2 AND scope_id = $3 AND state_key = $4;

-- name: UpsertPluginStateEntry :exec
INSERT INTO plugin_state (plugin_id, scope_kind, scope_id, state_key, value, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT(plugin_id, scope_kind, scope_id, state_key)
DO UPDATE SET value = excluded.value, updated_at = now();

-- name: DeletePluginStateEntry :exec
DELETE FROM plugin_state
WHERE plugin_id = $1 AND scope_kind = $2 AND scope_id = $3 AND state_key = $4;
