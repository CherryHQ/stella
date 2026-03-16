-- name: GetChannel :one
SELECT * FROM channels WHERE id = ?;

-- name: UpsertChannel :exec
INSERT INTO channels (id, enabled, config, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    enabled = excluded.enabled,
    config = excluded.config,
    updated_at = datetime('now');

-- name: ListChannels :many
SELECT * FROM channels ORDER BY id;
