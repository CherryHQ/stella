-- name: GetPlugin :one
SELECT * FROM plugin WHERE id = $1;

-- name: ListPlugins :many
SELECT * FROM plugin ORDER BY kind, name;

-- name: ListPluginsByKind :many
SELECT * FROM plugin WHERE kind = $1 ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM plugin WHERE enabled = true ORDER BY kind, name;

-- name: UpsertPlugin :exec
INSERT INTO plugin (id, kind, name, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    name = excluded.name,
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = now();

-- name: ListPluginOverrides :many
SELECT * FROM plugin ORDER BY kind, name;

-- name: DeletePlugin :exec
DELETE FROM plugin WHERE id = $1;
