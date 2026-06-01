-- name: GetManifestPluginOverride :one
SELECT * FROM plugin_override
WHERE plugin_id = ?;

-- name: ListManifestPluginOverrides :many
SELECT * FROM plugin_override
ORDER BY plugin_id;

-- name: UpsertManifestPluginOverride :exec
INSERT INTO plugin_override (plugin_id, enabled, session_env_vault_key, config, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(plugin_id) DO UPDATE SET
    enabled               = excluded.enabled,
    session_env_vault_key = excluded.session_env_vault_key,
    config                = excluded.config,
    updated_at            = datetime('now');

-- name: DeleteManifestPluginOverride :exec
DELETE FROM plugin_override
WHERE plugin_id = ?;
