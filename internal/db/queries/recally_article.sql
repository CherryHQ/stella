-- name: CreateArticle :one
INSERT INTO recally_article (
    id, user_id, agent_id, url, canonical_url, source_type,
    title, author, summary, tags, status, starred, file_path, metadata,
    published_at, saved_at, read_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING *;

-- name: GetArticle :one
SELECT * FROM recally_article WHERE id = $1 AND user_id = $2;

-- name: GetArticleByCanonicalURL :one
SELECT * FROM recally_article WHERE user_id = $1 AND canonical_url = $2;

-- name: ListArticles :many
SELECT * FROM recally_article
WHERE user_id = sqlc.arg('user_id')
  AND (sqlc.arg('status') = '' OR status = sqlc.arg('status'))
  AND (sqlc.arg('source_type') = '' OR source_type = sqlc.arg('source_type'))
  AND (sqlc.arg('starred')::bigint = 0 OR starred = (sqlc.arg('starred')::bigint = 1))
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
    updated_at  = now()
WHERE id = sqlc.arg('id') AND user_id = sqlc.arg('user_id')
RETURNING *;

-- name: DeleteArticle :exec
DELETE FROM recally_article WHERE id = $1 AND user_id = $2;

-- name: SearchArticles :many
-- Multi-field BM25 search over title/summary/tags/author via pg_search. Each
-- field is matched with paradedb.match, which tokenizes the raw user text with
-- ICU (CJK matches natively, no fallback tier) and never errors on punctuation.
-- Title is weighted above the other fields (the old setweight A=title intent) by
-- boosting the row score when the title matches, so a title hit outranks a body
-- or author hit. snippet highlights the title. The match arg is raw user text.
SELECT
    sqlc.embed(a),
    -- snippet highlights the title; NULL when the hit was on another field only.
    COALESCE(paradedb.snippet(a.title), '')::text AS snippet,
    (paradedb.score(a.id) * (CASE WHEN a.id @@@ paradedb.match('title', sqlc.arg('match')::text) THEN 3 ELSE 1 END))::double precision AS score
FROM recally_article a
WHERE (a.id @@@ paradedb.match('title', sqlc.arg('match')::text)
    OR a.id @@@ paradedb.match('summary', sqlc.arg('match')::text)
    OR a.id @@@ paradedb.match('tags', sqlc.arg('match')::text)
    OR a.id @@@ paradedb.match('author', sqlc.arg('match')::text))
  AND a.user_id = sqlc.arg('user_id')
ORDER BY score DESC
LIMIT sqlc.arg('limit');

-- name: CountArticlesByStatus :one
SELECT COUNT(*) as count FROM recally_article WHERE user_id = $1 AND status = $2;

-- name: CountStarredArticles :one
SELECT COUNT(*) as count FROM recally_article WHERE user_id = $1 AND starred = true;

-- name: ListArticlesSavedYesterday :many
SELECT * FROM recally_article
WHERE user_id = $1
  AND (saved_at AT TIME ZONE 'UTC')::date = (now() AT TIME ZONE 'UTC')::date - 1
ORDER BY saved_at DESC;

-- name: ListUnreadArticlesOlderThan :many
SELECT * FROM recally_article
WHERE user_id = sqlc.arg('user_id')
  AND status = 'unread'
  AND saved_at < sqlc.arg('cutoff')
ORDER BY saved_at ASC
LIMIT sqlc.arg('limit');

-- name: GetArticlesSavedThisWeek :many
SELECT * FROM recally_article
WHERE user_id = $1
  AND saved_at >= now() - interval '7 days'
ORDER BY saved_at DESC;
