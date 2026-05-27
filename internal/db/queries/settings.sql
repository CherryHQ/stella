-- name: GetSetting :one
SELECT * FROM settings WHERE key = ? AND org_id = ?;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, org_id, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(key, org_id) DO UPDATE SET
    value = excluded.value,
    updated_at = datetime('now');

-- name: ListSettings :many
SELECT * FROM settings WHERE org_id = ? ORDER BY key;

-- name: DeleteSetting :exec
DELETE FROM settings WHERE key = ? AND org_id = ?;
