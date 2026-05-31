-- name: GetPlugin :one
SELECT * FROM plugin WHERE id = ?;

-- name: ListPlugins :many
SELECT * FROM plugin ORDER BY kind, name;

-- name: ListPluginsByKind :many
SELECT * FROM plugin WHERE kind = ? ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM plugin WHERE enabled = 1 ORDER BY kind, name;

-- name: UpsertPlugin :exec
INSERT INTO plugin (id, kind, name, enabled, config, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    name = excluded.name,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListPluginOverrides :many
SELECT * FROM plugin ORDER BY kind, name;

-- name: DeletePlugin :exec
DELETE FROM plugin WHERE id = ?;
