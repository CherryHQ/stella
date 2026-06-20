-- name: CreateFeed :one
INSERT INTO recally_feed (
    id, user_id, agent_id, url, kind, metadata, title, description, check_interval,
    last_checked_at, last_etag, last_modified, enabled
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetFeed :one
SELECT * FROM recally_feed WHERE id = $1 AND user_id = $2;

-- name: GetFeedByURL :one
SELECT * FROM recally_feed WHERE user_id = $1 AND url = $2;

-- name: ListFeeds :many
SELECT * FROM recally_feed
WHERE user_id = sqlc.arg('user_id')
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateFeed :one
UPDATE recally_feed
SET title           = sqlc.arg('title'),
    description     = sqlc.arg('description'),
    metadata        = sqlc.arg('metadata'),
    check_interval  = sqlc.arg('check_interval'),
    last_checked_at = sqlc.arg('last_checked_at'),
    last_etag       = sqlc.arg('last_etag'),
    last_modified   = sqlc.arg('last_modified'),
    enabled         = sqlc.arg('enabled'),
    updated_at      = now()
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id')
RETURNING *;

-- name: DeleteFeed :exec
DELETE FROM recally_feed WHERE id = $1 AND user_id = $2;

-- name: CreateFeedEntry :one
INSERT INTO recally_feed_entry (
    id, feed_id, guid, url, title, status, article_id, attempts, error_msg,
    discovered_at, processed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT(feed_id, guid) DO NOTHING
RETURNING *;

-- name: GetFeedEntry :one
SELECT * FROM recally_feed_entry WHERE id = $1 AND feed_id = $2;

-- name: ListFeedEntries :many
SELECT * FROM recally_feed_entry
WHERE feed_id = sqlc.arg('feed_id')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
ORDER BY discovered_at DESC
LIMIT sqlc.arg('limit');

-- name: ListPendingEntries :many
SELECT * FROM recally_feed_entry
WHERE feed_id = sqlc.arg('feed_id')
  AND status IN ('pending', 'error')
  AND attempts < 3
ORDER BY discovered_at ASC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateFeedEntry :one
UPDATE recally_feed_entry
SET status       = sqlc.arg('status'),
    article_id   = sqlc.arg('article_id'),
    attempts     = attempts + 1,
    error_msg    = sqlc.arg('error_msg'),
    processed_at = now()
WHERE id = sqlc.arg('id') AND feed_id = sqlc.arg('feed_id')
RETURNING *;

-- name: DeleteOldEntries :exec
DELETE FROM recally_feed_entry
WHERE feed_id = $1
  AND status IN ('skipped', 'error')
  AND processed_at < now() - interval '30 days';
