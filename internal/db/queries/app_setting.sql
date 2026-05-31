-- name: GetSetting :one
SELECT * FROM app_setting WHERE key = ?;

-- name: UpsertSetting :exec
INSERT INTO app_setting (key, value, updated_at)
VALUES (?, ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = datetime('now');

-- name: ListSettings :many
SELECT * FROM app_setting ORDER BY key;

-- name: DeleteSetting :exec
DELETE FROM app_setting WHERE key = ?;
