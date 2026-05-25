-- name: GetPlugin :one
SELECT * FROM settings_plugins WHERE id = ?;

-- name: ListPlugins :many
SELECT * FROM settings_plugins ORDER BY kind, name;

-- name: ListPluginsByKind :many
SELECT * FROM settings_plugins WHERE kind = ? ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM settings_plugins WHERE enabled = 1 ORDER BY kind, name;

-- name: UpsertPlugin :exec
INSERT INTO settings_plugins (id, kind, name, enabled, config, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    name = excluded.name,
    enabled = excluded.enabled,
    config = excluded.config,
    org_id = excluded.org_id,
    updated_at = datetime('now');

-- name: SeedPlugin :exec
INSERT OR IGNORE INTO settings_plugins (id, kind, name, enabled, config, org_id)
VALUES (?, ?, ?, ?, ?, ?);

-- name: UpdatePluginEnabled :exec
UPDATE settings_plugins SET enabled = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: UpdatePluginConfig :exec
UPDATE settings_plugins SET config = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: DeletePlugin :exec
DELETE FROM settings_plugins WHERE id = ?;
