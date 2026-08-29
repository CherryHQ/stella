-- name: CreateMessage :one
INSERT INTO ctx_message (
    id, conversation_id, seq, role, event_type, content, token_count,
    actor_type, actor_id, source_session_id, inbox_id, origin_group_message_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, sqlc.narg('inbox_id'), sqlc.narg('origin_group_message_id'))
RETURNING *;

-- name: GetMessage :one
SELECT * FROM ctx_message WHERE id = $1 AND conversation_id = $2;

-- name: GetMessageByConversationOrigin :one
SELECT * FROM ctx_message
WHERE conversation_id = sqlc.arg(conversation_id)
  AND origin_group_message_id = sqlc.arg(origin_group_message_id);

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
  AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
  AND c.archived = false;

-- name: GetMessagesByConversation :many
SELECT * FROM ctx_message WHERE conversation_id = $1 ORDER BY seq ASC;

-- name: GetConversationTimeBounds :one
SELECT MIN(created_at) AS earliest_at, MAX(created_at) AS latest_at
FROM ctx_message
WHERE conversation_id = $1;

-- name: GetContextTimeBounds :one
SELECT oldest.created_at AS oldest_at, newest.created_at AS newest_at
FROM ctx_message oldest
JOIN LATERAL (
    SELECT newest_message.created_at
    FROM ctx_message newest_message
    WHERE newest_message.conversation_id = sqlc.arg('conversation_id')
    ORDER BY newest_message.seq DESC
    LIMIT 1
) newest ON true
WHERE oldest.conversation_id = sqlc.arg('conversation_id')
ORDER BY oldest.seq ASC
LIMIT 1;

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
SELECT id, conversation_id, seq, role, event_type, content, token_count, created_at,
       actor_type, actor_id, source_session_id, inbox_id, origin_group_message_id
FROM grouped
WHERE logical_idx IN (SELECT logical_idx FROM selected_groups)
ORDER BY seq ASC;

-- name: ListSessionTranscriptPage :many
-- Agent-facing transcript pages use a whole user turn as the atomic boundary:
-- one user row plus every assistant/tool row until the next user row. This keeps
-- tool calls and their results together even when a page boundary falls nearby.
WITH ordered AS (
    SELECT
        *,
        lag(seq) OVER (ORDER BY seq ASC) AS prev_seq
    FROM ctx_message
    WHERE conversation_id = sqlc.arg('conversation_id')
      AND (
          sqlc.narg('snapshot_seq')::bigint IS NULL
          OR seq <= sqlc.narg('snapshot_seq')::bigint
      )
), grouped AS (
    SELECT
        *,
        sum(CASE WHEN role = 'user' OR prev_seq IS NULL THEN 1 ELSE 0 END)
            OVER (ORDER BY seq ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS turn_idx
    FROM ordered
), anchor AS (
    SELECT COALESCE(
        (
            SELECT turn_idx
            FROM grouped
            WHERE sqlc.narg('anchor_seq')::bigint IS NULL
               OR seq <= sqlc.narg('anchor_seq')::bigint
            ORDER BY seq DESC
            LIMIT 1
        ),
        0
    ) AS turn_idx
), selected_turns AS (
    SELECT turn_idx
    FROM grouped
    WHERE turn_idx <= (SELECT turn_idx FROM anchor)
    GROUP BY turn_idx
    ORDER BY turn_idx DESC
    LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset')
)
SELECT id, conversation_id, seq, role, event_type, content, token_count, created_at,
       actor_type, actor_id, source_session_id, inbox_id, origin_group_message_id
FROM grouped
WHERE turn_idx IN (SELECT turn_idx FROM selected_turns)
ORDER BY seq ASC;

-- name: GetMessagesByConversationRange :many
SELECT * FROM ctx_message
WHERE conversation_id = $1 AND seq >= $2 AND seq <= $3
ORDER BY seq ASC;

-- name: GetMessageCount :one
SELECT COUNT(*) FROM ctx_message WHERE conversation_id = $1;

-- name: GetMaxSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS BIGINT) FROM ctx_message WHERE conversation_id = $1;

-- name: CreateMessagePart :one
INSERT INTO ctx_message_part (
    id, message_id, part_type, ordinal, media_id, text_content,
    tool_call_id, tool_name, tool_input, tool_output, metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListMessagesByIDs :many
SELECT * FROM ctx_message WHERE conversation_id = $1 AND id = ANY(sqlc.arg('message_ids')::uuid[]) ORDER BY seq ASC;

-- name: ListRecallMessageByIDs :many
-- Recall hits are untrusted hints spanning authorized conversations. Return
-- only identity and ownership facts so the PEP can match each hit back to the
-- exact conversation authorized in the same search operation.
SELECT id, conversation_id
FROM ctx_message
WHERE id = ANY(sqlc.arg('message_ids')::uuid[])
ORDER BY id;

-- name: GetMessageParts :many
SELECT * FROM ctx_message_part WHERE message_id = $1 ORDER BY ordinal ASC;

-- name: GetMessagePartsByMessages :many
-- The media join is the only source of an image part's baseline: the baseline
-- lives on ctx_media, so one image forwarded into two messages is described
-- once. It is a LEFT JOIN because media_id is nullable and becomes NULL when the
-- media is deleted, which is exactly how a reader learns the image is gone.
SELECT sqlc.embed(p), m.baseline AS media_baseline
FROM ctx_message_part p
LEFT JOIN ctx_media m ON m.id = p.media_id
WHERE p.message_id = ANY(sqlc.arg('message_ids')::uuid[])
ORDER BY p.message_id, p.ordinal ASC;

-- name: ListMessagePartsWithMediaByMessages :many
-- This returns media-backed parts only; GetMessagePartsByMessages remains the
-- batch loader for text and tool parts. The stable order avoids an N+1 media
-- lookup when Phase 3 reconstructs image-bearing messages.
SELECT sqlc.embed(p), sqlc.embed(m)
FROM ctx_message_part p
JOIN ctx_media m ON m.id = p.media_id
WHERE p.message_id = ANY(sqlc.arg('message_ids')::uuid[])
ORDER BY p.message_id, p.ordinal;

-- name: SearchMessages :many
-- Spans every active conversation of the current (user_id, agent_id). Joins
-- ctx_conversation to pin the scope and surface
-- provenance (session_id, title). Global ORDER BY score + LIMIT keeps the best
-- matches across the merged corpus, not per-conversation truncations. Lexical
-- ranking is pg_search BM25; paradedb.match tokenizes the raw user text with the
-- jieba tokenizer (dictionary + statistical CJK word segmentation, so CJK matches
-- natively with no fallback tier) and never errors on punctuation or query-syntax
-- characters. The match arg is the raw user text. Multi-term queries are OR'd and
-- ranked by BM25, which sinks weak partial matches below strong full matches.
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
  AND c.archived = false
-- created_at, id break score ties deterministically: cross-source RRF uses each
-- row's rank, so a stable order keeps the same query from reshuffling the merge.
ORDER BY score DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg('limit');

-- name: UpsertMessageEmbedding :exec
-- Write or replace the semantic-search vector for one message. model is the
-- vector-space key every KNN query must filter on; content_hash is the hash of
-- the embedded text so a later pass can spot rows whose source content changed.
-- Keep this comment ASCII (sqlc rewriter offsets).
INSERT INTO ctx_message_embedding (message_id, model, content_hash, embedding)
VALUES (sqlc.arg('message_id'), sqlc.arg('model'), sqlc.arg('content_hash'), sqlc.arg('embedding')::vector(1536))
ON CONFLICT (message_id) DO UPDATE
SET model        = EXCLUDED.model,
    content_hash = EXCLUDED.content_hash,
    embedding    = EXCLUDED.embedding,
    updated_at   = now();

-- name: ListMessagesNeedingEmbedding :many
-- Backfill candidates: messages with no embedding, or one produced by a
-- different model (a model switch invalidates the old vector space). Messages
-- are immutable, so a row whose model already matches is never stale and is not
-- returned. embedded_model/embedded_hash carry existing provenance so the writer
-- can skip rows already current. Keep ASCII.
SELECT m.id, m.content, e.model AS embedded_model, e.content_hash AS embedded_hash
FROM ctx_message m
JOIN ctx_conversation c ON c.id = m.conversation_id
LEFT JOIN ctx_message_embedding e ON e.message_id = m.id
WHERE c.archived = false
  AND (e.message_id IS NULL OR e.model <> sqlc.arg('model'))
ORDER BY m.created_at DESC
LIMIT sqlc.arg('limit');

-- name: SearchMessageEmbeddings :many
-- Exact KNN over the complete authorized candidate set. Materializing scope,
-- archive, and model filters before ordering deliberately prevents a global
-- approximate index walk from dropping legal rows behind foreign candidates.
-- The complete SQL order makes every LIMIT deterministic, including distance
-- ties, before the Go fusion layer sees the results.
WITH candidates AS MATERIALIZED (
    SELECT
        e.message_id,
        e.embedding,
        m.created_at
    FROM ctx_message_embedding e
    JOIN ctx_message m ON m.id = e.message_id
    JOIN ctx_conversation c ON c.id = m.conversation_id
    WHERE e.model = sqlc.arg('model')
      AND c.user_id = sqlc.arg('user_id')
      AND c.agent_id IS NOT DISTINCT FROM sqlc.narg('agent_id')
      AND c.archived = false
      -- Old deployments may already contain zero sidecars, which have no
      -- cosine direction and otherwise produce NaN distances under exact KNN.
      AND vector_norm(e.embedding) > 0
)
SELECT
    m.*,
    c.session_id AS session_id,
    c.title AS conversation_title,
    (1 - (e.embedding <=> sqlc.arg('query')::vector(1536)))::double precision AS score
FROM candidates e
JOIN ctx_message m ON m.id = e.message_id
JOIN ctx_conversation c ON c.id = m.conversation_id
ORDER BY e.embedding <=> sqlc.arg('query')::vector(1536), e.created_at DESC, e.message_id DESC
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
