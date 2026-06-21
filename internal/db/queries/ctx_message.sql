-- name: CreateMessage :one
INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetMessage :one
SELECT * FROM ctx_message WHERE id = $1 AND conversation_id = $2;

-- name: GetMessageScoped :one
-- Fetch one message in full by ID, scoped to (user_id, agent_id) across every
-- session: the read-in-full companion to cross-session SearchMessages. Joins
-- ctx_conversation for the same isolation filter plus provenance. Keep this doc
-- comment ASCII; multibyte chars corrupt sqlc's query rewriter offsets.
SELECT
    m.*,
    c.session_id AS session_id,
    c.title AS conversation_title
FROM ctx_message m
JOIN ctx_conversation c ON c.id = m.conversation_id
WHERE m.id = sqlc.arg('id')
  AND c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id');

-- name: GetMessagesByConversation :many
SELECT * FROM ctx_message WHERE conversation_id = $1 ORDER BY seq ASC;

-- name: GetConversationTimeBounds :one
SELECT MIN(created_at) AS earliest_at, MAX(created_at) AS latest_at
FROM ctx_message
WHERE conversation_id = $1;

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
      AND (sqlc.narg('after')::timestamptz IS NULL OR created_at >= sqlc.narg('after'))
      AND (sqlc.narg('before')::timestamptz IS NULL OR created_at <= sqlc.narg('before'))
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
WHERE conversation_id = $1 AND seq >= $2 AND seq <= $3
ORDER BY seq ASC;

-- name: GetMessageCount :one
SELECT COUNT(*) FROM ctx_message WHERE conversation_id = $1;

-- name: GetMaxSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS BIGINT) FROM ctx_message WHERE conversation_id = $1;

-- name: CreateMessagePart :exec
INSERT INTO ctx_message_part (id, message_id, part_type, ordinal, text_content, tool_call_id, tool_name, tool_input, tool_output, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListMessagesByIDs :many
SELECT * FROM ctx_message WHERE conversation_id = $1 AND id = ANY(sqlc.arg('message_ids')::uuid[]) ORDER BY seq ASC;

-- name: GetMessageParts :many
SELECT * FROM ctx_message_part WHERE message_id = $1 ORDER BY ordinal ASC;

-- name: GetMessagePartsByMessages :many
SELECT * FROM ctx_message_part WHERE message_id = ANY(sqlc.arg('message_ids')::uuid[]) ORDER BY message_id, ordinal ASC;

-- name: SearchMessages :many
-- Spans every conversation of the current (user_id, agent_id) so memory recall
-- survives across sessions. Joins ctx_conversation to pin the scope and surface
-- provenance (session_id, title). Global ORDER BY score + LIMIT keeps the best
-- matches across the merged corpus, not per-conversation truncations. Lexical
-- ranking is pg_search BM25; paradedb.match tokenizes the raw user text with ICU
-- (so CJK matches natively, no fallback tier) and never errors on punctuation or
-- query-syntax characters. The match arg is the raw user text.
SELECT
    m.*,
    c.session_id AS session_id,
    c.title AS conversation_title,
    paradedb.snippet(m.content)::text AS snippet,
    paradedb.score(m.id)::double precision AS score
FROM ctx_message m
JOIN ctx_conversation c ON c.id = m.conversation_id
WHERE m.id @@@ paradedb.match('content', sqlc.arg('match')::text)
  AND c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
ORDER BY score DESC
LIMIT sqlc.arg('limit');

-- name: GetMessagesSince :many
SELECT * FROM ctx_message
WHERE conversation_id = $1 AND created_at > $2
ORDER BY seq ASC;

-- name: ListExistingUserMessageContent :many
SELECT content FROM ctx_message
WHERE conversation_id = sqlc.arg('conversation_id')
  AND role = 'user'
  AND content = ANY(sqlc.arg('contents')::text[])
ORDER BY seq ASC;
