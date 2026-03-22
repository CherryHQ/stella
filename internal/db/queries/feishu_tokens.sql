-- name: GetFeishuToken :one
SELECT * FROM feishu_tokens WHERE open_id = ?;

-- name: UpsertFeishuToken :exec
INSERT INTO feishu_tokens (open_id, access_token, refresh_token, expires_at, refresh_expires_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(open_id) DO UPDATE SET
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    expires_at = excluded.expires_at,
    refresh_expires_at = excluded.refresh_expires_at,
    updated_at = datetime('now');

-- name: DeleteFeishuToken :exec
DELETE FROM feishu_tokens WHERE open_id = ?;
