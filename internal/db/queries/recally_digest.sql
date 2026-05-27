-- name: CreateDigest :one
INSERT INTO recally_digest (
    id, user_id, date, narrative,
    saved_yesterday_count, unread_count, read_count, archived_count,
    starred_count, worth_revisiting_count, total_articles, top_tags
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpsertDigest :one
INSERT INTO recally_digest (
    id, user_id, date, narrative,
    saved_yesterday_count, unread_count, read_count, archived_count,
    starred_count, worth_revisiting_count, total_articles, top_tags
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    updated_at             = datetime('now')
RETURNING *;

-- name: GetDigestByDate :one
SELECT * FROM recally_digest WHERE user_id = ? AND date = ?;

-- name: ListDigests :many
SELECT * FROM recally_digest
WHERE user_id = ?1
ORDER BY date DESC
LIMIT ?2 OFFSET ?3;

-- name: CountDigests :one
SELECT COUNT(*) FROM recally_digest WHERE user_id = ?;

-- name: AddDigestArticle :exec
INSERT INTO recally_digest_articles (digest_id, article_id, section, position)
VALUES (?, ?, ?, ?)
ON CONFLICT(digest_id, article_id, section) DO NOTHING;

-- name: DeleteDigestArticles :exec
DELETE FROM recally_digest_articles WHERE digest_id = ? AND section = ?;

-- name: ListDigestArticles :many
SELECT a.*
FROM recally_digest_articles da
JOIN recally_article a ON a.id = da.article_id
WHERE da.digest_id = ? AND da.section = ?
ORDER BY da.position ASC;
