-- name: GetPluginStateEntry :one
SELECT value FROM plugin_state
WHERE plugin_id = ? AND scope_kind = ? AND scope_id = ? AND state_key = ?;

-- name: UpsertPluginStateEntry :exec
INSERT INTO plugin_state (plugin_id, scope_kind, scope_id, state_key, value, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(plugin_id, scope_kind, scope_id, state_key)
DO UPDATE SET value = excluded.value, updated_at = datetime('now');

-- name: DeletePluginStateEntry :exec
DELETE FROM plugin_state
WHERE plugin_id = ? AND scope_kind = ? AND scope_id = ? AND state_key = ?;
