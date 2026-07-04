-- name: DeleteExpiredEmailSendDedup :exec
DELETE FROM email_send_dedup
WHERE sent_at < now() - interval '24 hours';

-- name: CreateEmailSendDedup :one
INSERT INTO email_send_dedup (user_id, idempotency_key)
VALUES ($1, $2)
ON CONFLICT (user_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetEmailSendDedup :one
SELECT * FROM email_send_dedup
WHERE user_id = $1 AND idempotency_key = $2;

-- name: DeleteEmailSendDedup :exec
DELETE FROM email_send_dedup
WHERE user_id = $1 AND idempotency_key = $2;
