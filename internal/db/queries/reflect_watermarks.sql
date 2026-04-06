-- name: GetReflectWatermark :one
SELECT reviewed_at FROM reflect_watermarks WHERE session_id = ?;

-- name: UpsertReflectWatermark :exec
INSERT INTO reflect_watermarks (session_id, reviewed_at)
VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET reviewed_at = excluded.reviewed_at;

-- name: DeleteReflectWatermark :exec
DELETE FROM reflect_watermarks WHERE session_id = ?;
