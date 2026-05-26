-- name: GetPlugin :one
SELECT * FROM settings_plugin WHERE id = ?;

-- name: ListPlugins :many
SELECT * FROM settings_plugin ORDER BY kind, name;

-- name: ListPluginsByKind :many
SELECT * FROM settings_plugin WHERE kind = ? ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM settings_plugin WHERE enabled = 1 ORDER BY kind, name;

-- name: UpsertPlugin :exec
INSERT INTO settings_plugin (id, kind, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    name = excluded.name,
    enabled = excluded.enabled,
    config = excluded.config,
    org_id = excluded.org_id,
    updated_at = datetime('now');

-- name: SeedPlugin :exec
INSERT OR IGNORE INTO settings_plugin (id, kind, name, enabled, config, org_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdatePluginEnabled :exec
UPDATE settings_plugin SET enabled = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: UpdatePluginConfig :exec
UPDATE settings_plugin SET config = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: DeletePlugin :exec
DELETE FROM settings_plugin WHERE id = ?;

-- name: ListPluginsByOrg :many
SELECT * FROM settings_plugin WHERE org_id = ? ORDER BY kind, name;

-- name: ListPluginsByKindAndOrg :many
SELECT * FROM settings_plugin WHERE kind = ? AND org_id = ? ORDER BY name;

-- name: ListEnabledPluginsByOrg :many
SELECT * FROM settings_plugin WHERE org_id = ? AND enabled = 1 ORDER BY kind, name;
