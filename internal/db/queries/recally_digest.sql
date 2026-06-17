-- name: CreateDigest :one
INSERT INTO recally_digest (
    id, user_id, date, narrative,
    saved_yesterday_count, unread_count, read_count, archived_count,
    starred_count, worth_revisiting_count, total_articles, top_tags
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpsertDigest :one
INSERT INTO recally_digest (
    id, user_id, date, narrative,
    saved_yesterday_count, unread_count, read_count, archived_count,
    starred_count, worth_revisiting_count, total_articles, top_tags
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT(user_id, date) DO UPDATE SET
    narrative              = excluded.narrative,
    saved_yesterday_count  = excluded.saved_yesterday_count,
    unread_count           = excluded.unread_count,
    read_count             = excluded.read_count,
    archived_count         = excluded.archived_count,
    starred_count          = excluded.starred_count,
    worth_revisiting_count = excluded.worth_revisiting_count,
    total_articles         = excluded.total_articles,
    top_tags               = excluded.top_tags,
    updated_at             = now()
RETURNING *;

-- name: GetDigestByDate :one
SELECT * FROM recally_digest WHERE user_id = $1 AND date = $2;

-- name: ListDigests :many
SELECT * FROM recally_digest
WHERE user_id = $1
ORDER BY date DESC
LIMIT $2 OFFSET $3;

-- name: CountDigests :one
SELECT COUNT(*) FROM recally_digest WHERE user_id = $1;

-- name: AddDigestArticle :exec
INSERT INTO recally_digest_article (digest_id, article_id, section, position)
VALUES ($1, $2, $3, $4)
ON CONFLICT(digest_id, article_id, section) DO NOTHING;

-- name: DeleteDigestArticles :exec
DELETE FROM recally_digest_article WHERE digest_id = $1 AND section = $2;

-- name: ListDigestArticles :many
SELECT a.*
FROM recally_digest_article da
JOIN recally_article a ON a.id = da.article_id
WHERE da.digest_id = $1 AND da.section = $2
ORDER BY da.position ASC;
