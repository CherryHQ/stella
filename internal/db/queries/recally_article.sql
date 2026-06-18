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
-- Weighted full-text search over title/summary/tags/author via the generated
-- search_tsv column (setweight A=title, B=summary/tags, C=author). ts_rank_cd's
-- default label weights rank title hits highest; ts_headline over title+summary
-- yields the snippet. Higher score = more relevant, hence ORDER BY score DESC.
-- TODO(Phase 5): validate ranking/snippet quality and the CJK trigram tier on
-- real PostgreSQL; CJK queries fall through to SearchArticlesLike (pg_trgm).
SELECT
    sqlc.embed(a),
    ts_headline('simple', coalesce(a.title, '') || ' ' || coalesce(a.summary, ''), websearch_to_tsquery('simple', sqlc.arg('match')), 'StartSel=<<,StopSel=>>,MaxFragments=1,MaxWords=32,MinWords=1')::text AS snippet,
    ts_rank_cd(a.search_tsv, websearch_to_tsquery('simple', sqlc.arg('match')))::double precision AS score
FROM recally_article a
WHERE a.search_tsv @@ websearch_to_tsquery('simple', sqlc.arg('match'))
  AND a.user_id = sqlc.arg('user_id')
ORDER BY score DESC
LIMIT sqlc.arg('limit');

-- name: SearchArticlesLike :many
-- Fallback for queries with no token of 3+ runes, which trigram MATCH would
-- silently never hit. Scans the content table directly, recency-ordered, no
-- ranking. Pattern must be a full '%text%' built with ftsquery.EscapeLike; see
-- SearchMessagesLike for the sqlc constraints shaping this query.
SELECT * FROM recally_article
WHERE user_id = sqlc.arg('user_id')
  AND ((title ILIKE sqlc.arg('pattern')::text ESCAPE '\')
    OR (summary ILIKE sqlc.arg('pattern')::text ESCAPE '\')
    OR (tags ILIKE sqlc.arg('pattern')::text ESCAPE '\')
    OR (author ILIKE sqlc.arg('pattern')::text ESCAPE '\'))
ORDER BY created_at DESC
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
