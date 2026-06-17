-- name: GetManifestPluginOverride :one
SELECT * FROM plugin_override
WHERE plugin_id = $1;

-- name: ListManifestPluginOverrides :many
SELECT * FROM plugin_override
ORDER BY plugin_id;

-- name: UpsertManifestPluginOverride :exec
INSERT INTO plugin_override (plugin_id, enabled, session_env_vault_key, config, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT(plugin_id) DO UPDATE SET
    enabled               = excluded.enabled,
    session_env_vault_key = excluded.session_env_vault_key,
    config                = excluded.config,
    updated_at            = now();

-- name: DeleteManifestPluginOverride :exec
DELETE FROM plugin_override
WHERE plugin_id = $1;
