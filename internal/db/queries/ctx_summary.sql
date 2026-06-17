-- name: CreateSummary :exec
INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at, descendant_count, descendant_token_count, source_message_token_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: GetSummary :one
SELECT * FROM ctx_summary WHERE id = $1 AND conversation_id = $2;

-- name: ListSummariesByIDs :many
SELECT * FROM ctx_summary WHERE conversation_id = $1 AND id = ANY(sqlc.arg('summary_ids')::text[]) ORDER BY created_at ASC;

-- name: GetSummaryByID :one
SELECT * FROM ctx_summary WHERE id = $1;

-- name: GetSummariesByConversation :many
SELECT * FROM ctx_summary WHERE conversation_id = $1 ORDER BY created_at ASC;

-- name: GetSummariesByDepth :many
SELECT * FROM ctx_summary
WHERE conversation_id = $1 AND depth = $2
ORDER BY created_at ASC;

-- name: LinkSummaryToMessage :exec
INSERT INTO ctx_summary_message (summary_id, message_id, ordinal)
VALUES ($1, $2, $3);

-- name: LinkSummaryToParent :exec
INSERT INTO ctx_summary_parent (summary_id, parent_summary_id, ordinal)
VALUES ($1, $2, $3);

-- name: ListSummaryParentsBySummaryIDs :many
SELECT summary_id, parent_summary_id, ordinal
FROM ctx_summary_parent
WHERE summary_id = ANY(sqlc.arg('summary_ids')::text[])
ORDER BY summary_id, ordinal;

-- name: GetSummaryMessages :many
SELECT m.* FROM ctx_message m
JOIN ctx_summary_message sm ON sm.message_id = m.id
WHERE sm.summary_id = $1
ORDER BY sm.ordinal ASC;

-- name: GetSummaryMessageSeqRange :one
SELECT
  CAST(COALESCE(MIN(m.seq), 0) AS BIGINT) AS message_seq_from,
  CAST(COALESCE(MAX(m.seq), 0) AS BIGINT) AS message_seq_to
FROM ctx_summary_message sm
JOIN ctx_message m ON sm.message_id = m.id
WHERE sm.summary_id = $1;

-- name: GetSummaryParents :many
SELECT s.* FROM ctx_summary s
JOIN ctx_summary_parent sp ON sp.parent_summary_id = s.id
WHERE sp.summary_id = $1
ORDER BY sp.ordinal ASC;

-- name: GetSummaryChildren :many
SELECT s.* FROM ctx_summary s
JOIN ctx_summary_parent sp ON sp.summary_id = s.id
WHERE sp.parent_summary_id = $1
ORDER BY sp.ordinal ASC;

-- name: SearchSummaries :many
-- Spans every conversation of the current (user_id, agent_id); see SearchMessages.
-- TODO(Phase 5): validate ranking/snippet quality and the CJK trigram tier on
-- real PostgreSQL; CJK queries fall through to SearchSummariesLike (pg_trgm).
SELECT
    s.*,
    c.session_id AS session_id,
    c.title AS conversation_title,
    ts_headline('simple', s.content, websearch_to_tsquery('simple', sqlc.arg('match')), 'StartSel=<<,StopSel=>>,MaxFragments=1,MaxWords=32,MinWords=1')::text AS snippet,
    ts_rank_cd(s.content_tsv, websearch_to_tsquery('simple', sqlc.arg('match')))::double precision AS score
FROM ctx_summary s
JOIN ctx_conversation c ON c.id = s.conversation_id
WHERE s.content_tsv @@ websearch_to_tsquery('simple', sqlc.arg('match'))
  AND c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
ORDER BY score DESC
LIMIT sqlc.arg('limit');

-- name: SearchSummariesLike :many
-- Fallback for queries with no token of 3+ runes (see SearchMessagesLike).
SELECT
    s.*,
    c.session_id AS session_id,
    c.title AS conversation_title
FROM ctx_summary s
JOIN ctx_conversation c ON c.id = s.conversation_id
WHERE c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
  AND (s.content LIKE sqlc.arg('pattern') ESCAPE '\')
ORDER BY s.created_at DESC
LIMIT sqlc.arg('limit');
