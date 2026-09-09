-- Migration-only plugin definition/configuration reads and import writes.
-- Runtime resource management is file-backed.

-- name: GetPluginDefinition :one
SELECT * FROM plugin_definition WHERE id = $1;

-- name: ListPluginDefinitions :many
SELECT * FROM plugin_definition ORDER BY id;

-- name: UpsertPluginDefinition :one
INSERT INTO plugin_definition (
    id, display_name, source,
    spec, default_enabled, revision, creator_user_id, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (id) DO UPDATE SET
    display_name = excluded.display_name,
    spec = excluded.spec,
    default_enabled = excluded.default_enabled,
    revision = CASE WHEN (plugin_definition.display_name, plugin_definition.spec, plugin_definition.default_enabled)
        IS DISTINCT FROM (excluded.display_name, excluded.spec, excluded.default_enabled)
        THEN plugin_definition.revision + 1 ELSE plugin_definition.revision END,
    updated_at = CASE WHEN (plugin_definition.display_name, plugin_definition.spec, plugin_definition.default_enabled)
        IS DISTINCT FROM (excluded.display_name, excluded.spec, excluded.default_enabled)
        THEN now() ELSE plugin_definition.updated_at END
WHERE plugin_definition.source = excluded.source
RETURNING *;

-- name: ListPluginConfigs :many
SELECT * FROM plugin_config
WHERE plugin_id = $1
ORDER BY scope, user_id NULLS FIRST, agent_id NULLS FIRST, id;

-- name: CreatePluginConfig :one
INSERT INTO plugin_config (
    id, plugin_id, scope, user_id, agent_id, enabled, config,
    credential_refs, revision, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
RETURNING *;

-- name: EnsureSystemPluginConfig :one
WITH inserted AS (
    INSERT INTO plugin_config (
        plugin_id, scope, enabled, config, credential_refs, revision, updated_at
    ) VALUES ($1, 'system', NULL, sqlc.arg(config)::jsonb, '{}'::jsonb, 1, now())
    ON CONFLICT (plugin_id, scope, user_id, agent_id) DO NOTHING
    RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT * FROM plugin_config
WHERE plugin_id = $1 AND scope = 'system'
LIMIT 1;

-- name: LockPluginCatalog :exec
SELECT pg_advisory_xact_lock(hashtextextended('plugin_cutover_v1', 0));
