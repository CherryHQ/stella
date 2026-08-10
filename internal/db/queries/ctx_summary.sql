-- name: CreateSummary :exec
INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at, descendant_count, descendant_token_count, source_message_token_count, contains_non_principal_input)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetSummary :one
SELECT * FROM ctx_summary WHERE id = $1 AND conversation_id = $2;

-- name: ListSummariesByIDs :many
SELECT * FROM ctx_summary WHERE conversation_id = $1 AND id = ANY(sqlc.arg('summary_ids')::text[]) ORDER BY created_at ASC;

-- name: ListRecallSummaryByIDs :many
-- As with recall messages, return only the resource ID and its exact owning
-- conversation for a bounded, fail-closed batch verification.
SELECT id, conversation_id
FROM ctx_summary
WHERE id = ANY(sqlc.arg('summary_ids')::text[])
ORDER BY id;

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

-- Legacy naming note: compaction stores the containing condensed summary in
-- summary_id and each constituent summary in parent_summary_id. Consequently,
-- GetSummaryParents returns conceptual children/constituents, while
-- GetSummaryChildren returns conceptual parents/containers.
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

-- name: ListSummariesNeedingEmbedding :many
-- Backfill candidates for summaries; see ListMessagesNeedingEmbedding. Summary
-- content is immutable, so missing or model-mismatch are the only cases. ASCII.
SELECT s.id, s.content, e.model AS embedded_model, e.content_hash AS embedded_hash
FROM ctx_summary s
LEFT JOIN ctx_summary_embedding e ON e.summary_id = s.id
WHERE e.summary_id IS NULL OR e.model <> sqlc.arg('model')
ORDER BY s.created_at DESC
LIMIT sqlc.arg('limit');

-- name: UpsertSummaryEmbedding :exec
-- Semantic-search vector for one summary; see UpsertMessageEmbedding. Keep ASCII.
INSERT INTO ctx_summary_embedding (summary_id, model, content_hash, embedding)
VALUES (sqlc.arg('summary_id'), sqlc.arg('model'), sqlc.arg('content_hash'), sqlc.arg('embedding')::vector(1536))
ON CONFLICT (summary_id) DO UPDATE
SET model        = EXCLUDED.model,
    content_hash = EXCLUDED.content_hash,
    embedding    = EXCLUDED.embedding,
    updated_at   = now();

-- name: SearchSummaryEmbeddings :many
-- Vector KNN over summary embeddings; mirror of SearchMessageEmbeddings (same
-- space + scope guards, cosine similarity score). Tie-break by content time then
-- id. Keep ASCII.
SELECT
    s.*,
    c.session_id AS session_id,
    c.title AS conversation_title,
    (1 - (e.embedding <=> sqlc.arg('query')::vector(1536)))::double precision AS score
FROM ctx_summary_embedding e
JOIN ctx_summary s ON s.id = e.summary_id
JOIN ctx_conversation c ON c.id = s.conversation_id
WHERE e.model = sqlc.arg('model')
  AND c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
ORDER BY e.embedding <=> sqlc.arg('query')::vector(1536), COALESCE(s.latest_at, s.created_at) DESC, s.id DESC
LIMIT sqlc.arg('limit');

-- name: SearchSummaries :many
-- Spans every conversation of the current (user_id, agent_id); see SearchMessages.
-- Lexical ranking is pg_search BM25; paradedb.match tokenizes the raw user text
-- with the jieba tokenizer (dictionary + statistical CJK word segmentation, CJK
-- matches natively) and never errors on punctuation. The match arg is the raw
-- user text.
SELECT
    s.*,
    c.session_id AS session_id,
    c.title AS conversation_title,
    paradedb.snippet(s.content)::text AS snippet,
    paradedb.score(s.id)::double precision AS score
FROM ctx_summary s
JOIN ctx_conversation c ON c.id = s.conversation_id
WHERE s.id @@@ paradedb.match('content', sqlc.arg('match')::text)
  AND c.user_id = sqlc.arg('user_id')
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
-- Stable score tie-break (see SearchMessages): content time, then id, so the
-- per-source rank that cross-source RRF consumes is deterministic.
ORDER BY score DESC, COALESCE(s.latest_at, s.created_at) DESC, s.id DESC
LIMIT sqlc.arg('limit');
