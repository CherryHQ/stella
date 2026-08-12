-- name: EnqueueSessionInbox :one
INSERT INTO ctx_session_inbox (
    id, source_session_id, target_session_id, actor_id, content
)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ClaimSessionInboxDelivery :one
-- Claim and validate the immutable delivery facts in one CAS. The enclosing LCM
-- transaction rolls this UPDATE back if any transcript write fails afterwards.
UPDATE ctx_session_inbox
SET delivered_at = now(), updated_at = now()
WHERE id = sqlc.arg('id')
  AND source_session_id = sqlc.arg('source_session_id')
  AND target_session_id = sqlc.arg('target_session_id')
  AND actor_id = sqlc.arg('actor_id')
  AND content = sqlc.arg('content')
  AND delivered_at IS NULL
  AND failed_at IS NULL
RETURNING *;

-- name: FailPendingSessionInbox :execrows
UPDATE ctx_session_inbox
SET failed_at = now(), error_code = sqlc.arg('error_code'), updated_at = now()
WHERE id = sqlc.arg('id')
  AND delivered_at IS NULL
  AND failed_at IS NULL;

-- name: ListPendingSessionInbox :many
SELECT *
FROM ctx_session_inbox
WHERE delivered_at IS NULL
  AND failed_at IS NULL
  AND enqueue_seq > sqlc.arg('after_enqueue_seq')::bigint
ORDER BY enqueue_seq
LIMIT sqlc.arg('page_size')::integer;

-- name: GetSessionInbox :one
SELECT * FROM ctx_session_inbox WHERE id = $1;
