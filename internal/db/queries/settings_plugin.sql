-- name: GetPlugin :one
SELECT * FROM settings_plugin WHERE id = ? AND org_id = ?;

-- name: ListPlugins :many
SELECT * FROM settings_plugin WHERE org_id = ? ORDER BY kind, name;

-- name: ListPluginsByKind :many
SELECT * FROM settings_plugin WHERE kind = ? AND org_id = ? ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM settings_plugin WHERE org_id = ? AND enabled = 1 ORDER BY kind, name;

-- name: UpsertPlugin :exec
INSERT INTO settings_plugin (id, kind, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id, org_id) DO UPDATE SET
    kind = excluded.kind,
    name = excluded.name,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListPluginOverrides :many
SELECT * FROM settings_plugin WHERE org_id = ? ORDER BY kind, name;

-- name: UpsertPluginEnabled :exec
INSERT INTO settings_plugin (id, kind, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, '{}', ?, datetime('now'))
ON CONFLICT(id, org_id) DO UPDATE SET
    enabled = excluded.enabled,
    updated_at = datetime('now');

-- name: UpsertPluginConfig :exec
INSERT INTO settings_plugin (id, kind, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, 0, ?, ?, datetime('now'))
ON CONFLICT(id, org_id) DO UPDATE SET
    config = excluded.config,
    updated_at = datetime('now');

-- name: DeletePlugin :exec
DELETE FROM settings_plugin WHERE id = ? AND org_id = ?;
