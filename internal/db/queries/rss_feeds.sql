-- name: CreateRSSFeed :one
INSERT INTO rss_feeds (
    id, user_id, agent_id, url, title, description, check_interval,
    last_checked_at, last_etag, last_modified, enabled
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRSSFeed :one
SELECT * FROM rss_feeds WHERE id = ?;

-- name: GetRSSFeedByURL :one
SELECT * FROM rss_feeds WHERE user_id = ? AND url = ?;

-- name: ListRSSFeeds :many
SELECT * FROM rss_feeds
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: UpdateRSSFeed :one
UPDATE rss_feeds
SET title           = sqlc.arg('title'),
    description     = sqlc.arg('description'),
    check_interval  = sqlc.arg('check_interval'),
    last_checked_at = sqlc.arg('last_checked_at'),
    last_etag       = sqlc.arg('last_etag'),
    last_modified   = sqlc.arg('last_modified'),
    enabled         = sqlc.arg('enabled'),
    updated_at      = datetime('now')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteRSSFeed :exec
DELETE FROM rss_feeds WHERE id = ?;

-- name: CreateRSSFeedEntry :one
INSERT INTO rss_feed_entries (
    id, feed_id, guid, url, title, status, article_id, attempts, error_msg,
    discovered_at, processed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(feed_id, guid) DO NOTHING
RETURNING *;

-- name: GetRSSFeedEntry :one
SELECT * FROM rss_feed_entries WHERE id = ?;

-- name: ListRSSFeedEntries :many
SELECT * FROM rss_feed_entries
WHERE feed_id = sqlc.arg('feed_id')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
ORDER BY discovered_at DESC
LIMIT sqlc.arg('limit');

-- name: ListPendingRSSEntries :many
SELECT * FROM rss_feed_entries
WHERE feed_id = sqlc.arg('feed_id')
  AND status IN ('pending', 'error')
  AND attempts < 3
ORDER BY discovered_at ASC
LIMIT sqlc.arg('limit');

-- name: UpdateRSSFeedEntry :one
UPDATE rss_feed_entries
SET status       = sqlc.arg('status'),
    article_id   = sqlc.arg('article_id'),
    attempts     = attempts + 1,
    error_msg    = sqlc.arg('error_msg'),
    processed_at = datetime('now')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteOldRSSEntries :exec
DELETE FROM rss_feed_entries
WHERE feed_id = ?
  AND status IN ('skipped', 'error')
  AND processed_at < datetime('now', '-30 days');
