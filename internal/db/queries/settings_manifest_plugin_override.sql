-- name: GetManifestPluginOverride :one
SELECT * FROM settings_manifest_plugin_override
WHERE plugin_id = ?;

-- name: ListManifestPluginOverrides :many
SELECT * FROM settings_manifest_plugin_override
ORDER BY plugin_id;

-- name: UpsertManifestPluginOverride :exec
INSERT INTO settings_manifest_plugin_override (plugin_id, enabled, session_env_vault_key, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(plugin_id) DO UPDATE SET
    enabled               = excluded.enabled,
    session_env_vault_key = excluded.session_env_vault_key,
    updated_at            = datetime('now');

-- name: DeleteManifestPluginOverride :exec
DELETE FROM settings_manifest_plugin_override
WHERE plugin_id = ?;
