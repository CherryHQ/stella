-- name: CreateArticle :one
INSERT INTO articles (
    id, user_id, agent_id, url, canonical_url, source_type,
    title, author, summary, tags, status, starred, file_path, metadata,
    published_at, saved_at, read_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetArticle :one
SELECT * FROM articles WHERE id = ?;

-- name: GetArticleByCanonicalURL :one
SELECT * FROM articles WHERE user_id = ? AND canonical_url = ?;

-- name: ListArticles :many
SELECT * FROM articles
WHERE user_id = ?
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
  AND (sqlc.arg('source_type') = '' OR source_type = sqlc.arg('source_type'))
  AND (sqlc.arg('starred') = 0 OR starred = sqlc.arg('starred'))
ORDER BY saved_at DESC
LIMIT sqlc.arg('limit');

-- name: UpdateArticle :one
UPDATE articles
SET title       = sqlc.arg('title'),
    author      = sqlc.arg('author'),
    summary     = sqlc.arg('summary'),
    tags        = sqlc.arg('tags'),
    status      = sqlc.arg('status'),
    starred     = sqlc.arg('starred'),
    file_path   = sqlc.arg('file_path'),
    metadata    = sqlc.arg('metadata'),
    published_at = sqlc.arg('published_at'),
    read_at     = sqlc.arg('read_at'),
    updated_at  = datetime('now')
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: DeleteArticle :exec
DELETE FROM articles WHERE id = ?;

-- name: SearchArticles :many
-- Phase 1 MVP: LIKE-based search. Upgrade to FTS5 in future phase.
SELECT * FROM articles
WHERE user_id = ?
  AND (title LIKE '%' || ? || '%'
       OR summary LIKE '%' || ? || '%'
       OR tags LIKE '%' || ? || '%'
       OR author LIKE '%' || ? || '%')
ORDER BY saved_at DESC
LIMIT ?;

-- name: CountArticlesByStatus :one
SELECT COUNT(*) as count FROM articles WHERE user_id = ? AND status = ?;

-- name: CountStarredArticles :one
SELECT COUNT(*) as count FROM articles WHERE user_id = ? AND starred = 1;

-- name: ListArticlesSavedYesterday :many
SELECT * FROM articles
WHERE user_id = ?
  AND date(saved_at) = date('now', '-1 day')
ORDER BY saved_at DESC;

-- name: ListUnreadArticlesOlderThan :many
SELECT * FROM articles
WHERE user_id = ?
  AND status = 'unread'
  AND saved_at < datetime('now', ?)
ORDER BY saved_at ASC
LIMIT ?;

-- name: GetArticlesSavedThisWeek :many
SELECT * FROM articles
WHERE user_id = ?
  AND saved_at >= datetime('now', '-7 days')
ORDER BY saved_at DESC;
