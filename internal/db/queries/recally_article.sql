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

-- name: GetArticleContent :one
SELECT content FROM recally_article_content WHERE article_id = $1;

-- name: UpsertArticleContent :exec
INSERT INTO recally_article_content (article_id, content)
VALUES (sqlc.arg('article_id'), sqlc.arg('content'))
ON CONFLICT (article_id) DO UPDATE
SET content = EXCLUDED.content,
    updated_at = now();

-- name: InsertArticleContentIfAbsent :exec
INSERT INTO recally_article_content (article_id, content)
VALUES (sqlc.arg('article_id'), sqlc.arg('content'))
ON CONFLICT (article_id) DO NOTHING;

-- name: ListArticlesMissingContent :many
-- Legacy articles whose body still lives only in a disk file (file_path set) and
-- was never copied into recally_article_content. Startup eager-backfills these so
-- bodies survive on hosts where the pod-local disk is gone. Keep ASCII.
SELECT a.id, a.user_id, a.file_path
FROM recally_article a
LEFT JOIN recally_article_content c ON c.article_id = a.id
WHERE a.file_path <> '' AND c.article_id IS NULL
ORDER BY a.saved_at DESC;

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
-- the jieba tokenizer (dictionary + statistical CJK word segmentation, CJK
-- matches natively with no fallback tier) and never errors on punctuation.
-- Per-field relevance is weighted natively with paradedb.boost (title 3x, tags 2x,
-- summary/author 1x): boost scales each field's BM25 contribution to the row
-- score, so a title hit outranks a tags hit outranks a body/author hit, with no
-- CASE multiplier on the whole row. snippet highlights the title. match is raw
-- user text.
SELECT
    sqlc.embed(a),
    -- snippet highlights the title; NULL when the hit was on another field only.
    COALESCE(paradedb.snippet(a.title), '')::text AS snippet,
    paradedb.score(a.id)::double precision AS score
FROM recally_article a
WHERE (a.id @@@ paradedb.boost(3.0, paradedb.match('title', sqlc.arg('match')::text))
    OR a.id @@@ paradedb.boost(2.0, paradedb.match('tags', sqlc.arg('match')::text))
    OR a.id @@@ paradedb.match('summary', sqlc.arg('match')::text)
    OR a.id @@@ paradedb.match('author', sqlc.arg('match')::text))
  AND a.user_id = sqlc.arg('user_id')
-- saved_at, id break score ties deterministically so identical queries return a
-- stable order.
ORDER BY score DESC, a.saved_at DESC, a.id DESC
LIMIT sqlc.arg('limit');

-- name: ListArticlesNeedingEmbedding :many
-- Backfill candidates for articles. Unlike messages, articles are mutable, so a
-- row whose embedding predates the article's last update (e.updated_at <
-- a.updated_at) is a candidate too; the writer then compares content_hash to
-- skip non-content edits (e.g. marking read). content is title + summary, the
-- fields worth embedding. Keep ASCII.
SELECT a.id, (a.title || ' ' || COALESCE(a.summary, ''))::text AS content, e.model AS embedded_model, e.content_hash AS embedded_hash
FROM recally_article a
LEFT JOIN recally_article_embedding e ON e.article_id = a.id
WHERE e.article_id IS NULL OR e.model <> sqlc.arg('model') OR e.updated_at < a.updated_at
ORDER BY a.saved_at DESC
LIMIT sqlc.arg('limit');

-- name: UpsertArticleEmbedding :exec
-- Semantic-search vector for one article; see UpsertMessageEmbedding. Keep ASCII.
INSERT INTO recally_article_embedding (article_id, model, content_hash, embedding)
VALUES (sqlc.arg('article_id'), sqlc.arg('model'), sqlc.arg('content_hash'), sqlc.arg('embedding')::vector(1536))
ON CONFLICT (article_id) DO UPDATE
SET model        = EXCLUDED.model,
    content_hash = EXCLUDED.content_hash,
    embedding    = EXCLUDED.embedding,
    updated_at   = now();

-- name: SearchArticleEmbeddings :many
-- Vector KNN over article embeddings, scoped to user_id (articles carry user_id
-- directly, no conversation join). WHERE model pins the vector space; ORDER BY
-- the <=> operator drives the HNSW cosine index. score is cosine similarity
-- (1 - distance), higher is better. saved_at, id break ties. Keep ASCII.
SELECT
    sqlc.embed(a),
    (1 - (e.embedding <=> sqlc.arg('query')::vector(1536)))::double precision AS score
FROM recally_article_embedding e
JOIN recally_article a ON a.id = e.article_id
WHERE e.model = sqlc.arg('model')
  AND a.user_id = sqlc.arg('user_id')
ORDER BY e.embedding <=> sqlc.arg('query')::vector(1536), a.saved_at DESC, a.id DESC
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
