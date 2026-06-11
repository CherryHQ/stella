-- name: CreateMessage :one
INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMessage :one
SELECT * FROM ctx_message WHERE id = ? AND conversation_id = ?;

-- name: GetMessagesByConversation :many
SELECT * FROM ctx_message WHERE conversation_id = ? ORDER BY seq ASC;

-- name: GetConversationTimeBounds :one
SELECT MIN(created_at) AS earliest_at, MAX(created_at) AS latest_at
FROM ctx_message
WHERE conversation_id = ?;

-- name: ListMessagesByLogicalPage :many
-- Keep this logical-message boundary in sync with serializeDBMessages in
-- internal/server/sessions.go: consecutive assistant rows render as one
-- response message, so SQL pagination must count them as one too.
WITH ordered AS (
    SELECT
        *,
        lag(role) OVER (ORDER BY seq ASC) AS prev_role
    FROM ctx_message
    WHERE conversation_id = sqlc.arg('conversation_id')
      AND (sqlc.narg('after') IS NULL OR created_at >= sqlc.narg('after'))
      AND (sqlc.narg('before') IS NULL OR created_at <= sqlc.narg('before'))
), grouped AS (
    SELECT
        *,
        sum(CASE WHEN role = 'assistant' AND prev_role = 'assistant' THEN 0 ELSE 1 END)
            OVER (ORDER BY seq ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS logical_idx
    FROM ordered
), selected_groups AS (
    SELECT logical_idx
    FROM grouped
    GROUP BY logical_idx
    ORDER BY logical_idx DESC
    LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset')
)
SELECT id, conversation_id, seq, role, event_type, content, token_count, created_at
FROM grouped
WHERE logical_idx IN (SELECT logical_idx FROM selected_groups)
ORDER BY seq ASC;

-- name: GetMessagesByConversationRange :many
SELECT * FROM ctx_message
WHERE conversation_id = ? AND seq >= ? AND seq <= ?
ORDER BY seq ASC;

-- name: GetMessageCount :one
SELECT COUNT(*) FROM ctx_message WHERE conversation_id = ?;

-- name: GetMaxSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM ctx_message WHERE conversation_id = ?;

-- name: CreateMessagePart :exec
INSERT INTO ctx_message_part (id, message_id, part_type, ordinal, text_content, tool_call_id, tool_name, tool_input, tool_output, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListMessagesByIDs :many
SELECT * FROM ctx_message WHERE conversation_id = ? AND id IN (sqlc.slice('message_ids')) ORDER BY seq ASC;

-- name: GetMessageParts :many
SELECT * FROM ctx_message_part WHERE message_id = ? ORDER BY ordinal ASC;

-- name: GetMessagePartsByMessages :many
SELECT * FROM ctx_message_part WHERE message_id IN (sqlc.slice('message_ids')) ORDER BY message_id, ordinal ASC;

-- name: SearchMessages :many
SELECT
    m.*,
    snippet(ctx_message_fts, 0, '<<', '>>', '...', 32) AS snippet,
    bm25(ctx_message_fts) AS score
FROM ctx_message_fts
JOIN ctx_message m ON m.rowid = ctx_message_fts.rowid
WHERE ctx_message_fts.content MATCH sqlc.arg('match')
  AND m.conversation_id = sqlc.arg('conversation_id')
ORDER BY score ASC
LIMIT sqlc.arg('limit');

-- name: SearchMessagesLike :many
-- Fallback for queries with no token of 3+ runes, which trigram MATCH would
-- silently never hit. Scans the content table directly (faster there than on
-- the FTS table, which pays external-content read-back), recency-ordered, no
-- BM25. Pattern must be a full '%text%' built with ftsquery.EscapeLike; sqlc
-- cannot parse || concatenation here, so the caller wraps it, and the parens
-- around LIKE...ESCAPE are also required by sqlc's grammar. Keep these doc
-- comments ASCII: multibyte chars corrupt sqlc's query rewriter offsets.
SELECT * FROM ctx_message
WHERE conversation_id = sqlc.arg('conversation_id')
  AND (content LIKE sqlc.arg('pattern') ESCAPE '\')
ORDER BY created_at DESC
LIMIT sqlc.arg('limit');

-- name: GetMessagesSince :many
SELECT * FROM ctx_message
WHERE conversation_id = ? AND created_at > ?
ORDER BY seq ASC;
