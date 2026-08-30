-- name: GetSetting :one
SELECT * FROM app_setting WHERE key = $1;

-- name: UpsertSetting :exec
INSERT INTO app_setting (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = now();

-- name: UpsertSettingIfValue :execrows
INSERT INTO app_setting (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), now())
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    updated_at = now()
WHERE app_setting.value = sqlc.arg(expected_value);

-- name: ListSettings :many
SELECT * FROM app_setting ORDER BY key;

-- name: DeleteSetting :exec
DELETE FROM app_setting WHERE key = $1;
