-- name: GetSetting :one
SELECT * FROM app_setting WHERE key = $1;

-- name: UpsertSetting :exec
INSERT INTO app_setting (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = now();

-- name: ListSettings :many
SELECT * FROM app_setting ORDER BY key;

-- name: DeleteSetting :exec
DELETE FROM app_setting WHERE key = $1;
