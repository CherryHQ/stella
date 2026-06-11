-- name: CreateArticle :one
INSERT INTO recally_article (
    id, user_id, agent_id, url, canonical_url, source_type,
    title, author, summary, tags, status, starred, file_path, metadata,
    published_at, saved_at, read_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetArticle :one
SELECT * FROM recally_article WHERE id = ? AND user_id = ?;

-- name: GetArticleByCanonicalURL :one
SELECT * FROM recally_article WHERE user_id = ? AND canonical_url = ?;

-- name: ListArticles :many
SELECT * FROM recally_article
WHERE user_id = sqlc.arg('user_id')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
  AND (sqlc.arg('source_type') = '' OR source_type = sqlc.arg('source_type'))
  AND (sqlc.arg('starred') = 0 OR starred = sqlc.arg('starred'))
ORDER BY saved_at DESC, id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: UpdateArticle :one
UPDATE recally_article
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
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id')
RETURNING *;

-- name: DeleteArticle :exec
DELETE FROM recally_article WHERE id = ? AND user_id = ?;

-- name: SearchArticles :many
-- FTS5/BM25 search over title/summary/tags/author. Matching against the
-- hidden table-name column searches every indexed column (declared for sqlc
-- in recally_article_fts_sqlc.sql); bm25 weights rank title hits highest,
-- and snippet column -1 picks the best-matching column. More negative bm25
-- means more relevant, hence ORDER BY score ASC.
SELECT
    sqlc.embed(a),
    snippet(recally_article_fts, -1, '<<', '>>', '...', 32) AS snippet,
    bm25(recally_article_fts, 4.0, 2.0, 2.0, 1.0) AS score
FROM recally_article_fts
JOIN recally_article a ON a.rowid = recally_article_fts.rowid
WHERE recally_article_fts.recally_article_fts MATCH sqlc.arg('match')
  AND a.user_id = sqlc.arg('user_id')
ORDER BY score ASC
LIMIT sqlc.arg('limit');

-- name: CountArticlesByStatus :one
SELECT COUNT(*) as count FROM recally_article WHERE user_id = ? AND status = ?;

-- name: CountStarredArticles :one
SELECT COUNT(*) as count FROM recally_article WHERE user_id = ? AND starred = 1;

-- name: ListArticlesSavedYesterday :many
SELECT * FROM recally_article
WHERE user_id = ?
  AND date(saved_at) = date('now', '-1 day')
ORDER BY saved_at DESC;

-- name: ListUnreadArticlesOlderThan :many
SELECT * FROM recally_article
WHERE user_id = ?
  AND status = 'unread'
  AND datetime(saved_at) < datetime('now', ?)
ORDER BY saved_at ASC
LIMIT ?;

-- name: GetArticlesSavedThisWeek :many
SELECT * FROM recally_article
WHERE user_id = ?
  AND datetime(saved_at) >= datetime('now', '-7 days')
ORDER BY saved_at DESC;
