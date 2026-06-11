-- name: CreateSummary :exec
INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at, descendant_count, descendant_token_count, source_message_token_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSummary :one
SELECT * FROM ctx_summary WHERE id = ? AND conversation_id = ?;

-- name: ListSummariesByIDs :many
SELECT * FROM ctx_summary WHERE conversation_id = ? AND id IN (sqlc.slice('summary_ids')) ORDER BY created_at ASC;

-- name: GetSummaryByID :one
SELECT * FROM ctx_summary WHERE id = ?;

-- name: GetSummariesByConversation :many
SELECT * FROM ctx_summary WHERE conversation_id = ? ORDER BY created_at ASC;

-- name: GetSummariesByDepth :many
SELECT * FROM ctx_summary
WHERE conversation_id = ? AND depth = ?
ORDER BY created_at ASC;

-- name: LinkSummaryToMessage :exec
INSERT INTO ctx_summary_message (summary_id, message_id, ordinal)
VALUES (?, ?, ?);

-- name: LinkSummaryToParent :exec
INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
VALUES (?, ?, ?);

-- name: GetSummaryMessages :many
SELECT m.* FROM ctx_message m
JOIN ctx_summary_message sm ON sm.message_id = m.id
WHERE sm.summary_id = ?
ORDER BY sm.ordinal ASC;

-- name: GetSummaryMessageSeqRange :one
SELECT
  CAST(COALESCE(MIN(m.seq), 0) AS INTEGER) AS message_seq_from,
  CAST(COALESCE(MAX(m.seq), 0) AS INTEGER) AS message_seq_to
FROM ctx_summary_message sm
JOIN ctx_message m ON sm.message_id = m.id
WHERE sm.summary_id = ?;

-- name: GetSummaryParents :many
SELECT s.* FROM ctx_summary s
JOIN ctx_summary_parent sp ON sp.parent_summary_id = s.id
WHERE sp.summary_id = ?
ORDER BY sp.ordinal ASC;

-- name: GetSummaryChildren :many
SELECT s.* FROM ctx_summary s
JOIN ctx_summary_parent sp ON sp.summary_id = s.id
WHERE sp.parent_summary_id = ?
ORDER BY sp.ordinal ASC;

-- name: SearchSummaries :many
SELECT
    s.*,
    snippet(ctx_summary_fts, 0, '<<', '>>', '...', 32) AS snippet,
    bm25(ctx_summary_fts) AS score
FROM ctx_summary_fts
JOIN ctx_summary s ON s.rowid = ctx_summary_fts.rowid
WHERE ctx_summary_fts.content MATCH sqlc.arg('match')
  AND s.conversation_id = sqlc.arg('conversation_id')
ORDER BY score ASC
LIMIT sqlc.arg('limit');

-- name: SearchSummariesLike :many
-- Fallback for queries with no token of 3+ runes (see SearchMessagesLike).
SELECT * FROM ctx_summary
WHERE conversation_id = sqlc.arg('conversation_id')
  AND (content LIKE sqlc.arg('pattern') ESCAPE '\')
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');
