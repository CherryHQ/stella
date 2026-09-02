-- name: GetPlugin :one
SELECT * FROM plugin WHERE id = $1;

-- name: ListPlugins :many
SELECT * FROM plugin ORDER BY kind, name;

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

-- Field-scoped writes. The whole-row UpsertPlugin above is a lost update when
-- two writers touch the same plugin: the admin kill switch and a channel's
-- credential mirror are exactly that pair. These update one column and leave
-- the other as it is; the VALUES list only decides what a first insert gets.

-- name: UpsertPluginEnabled :exec
INSERT INTO plugin (id, kind, name, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, '{}'::jsonb, now())
ON CONFLICT(id) DO UPDATE SET
    enabled = excluded.enabled,
    updated_at = now();

-- name: UpsertPluginConfig :exec
INSERT INTO plugin (id, kind, name, enabled, config, updated_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT(id) DO UPDATE SET
    config = excluded.config,
    updated_at = now();

-- name: ListPluginOverrides :many
SELECT * FROM plugin ORDER BY kind, name;

-- name: DeletePlugin :exec
DELETE FROM plugin WHERE id = $1;
