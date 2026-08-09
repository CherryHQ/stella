-- name: AppendContextItem :exec
INSERT INTO ctx_item (conversation_id, ordinal, item_type, message_id, summary_id, event_type, role)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetContextItems :many
SELECT * FROM ctx_item
WHERE conversation_id = $1
ORDER BY ordinal ASC;

-- name: ListContextItemsPage :many
SELECT
  ci.ordinal,
  ci.item_type,
  ci.event_type,
  ci.role,
  m.id AS message_id,
  m.seq AS message_seq,
  m.role AS message_role,
  m.event_type AS message_event_type,
  m.content AS message_content,
  m.token_count AS message_token_count,
  m.created_at AS message_created_at,
  m.actor_type AS message_actor_type,
  m.actor_id AS message_actor_id,
  m.source_session_id AS message_source_session_id,
  s.id AS summary_id,
  s.kind AS summary_kind,
  s.depth AS summary_depth,
  s.content AS summary_content,
  s.token_count AS summary_token_count,
  s.earliest_at AS summary_earliest_at,
  s.latest_at AS summary_latest_at,
  s.descendant_count AS summary_descendant_count,
  s.descendant_token_count AS summary_descendant_token_count,
  s.source_message_token_count AS summary_source_message_token_count,
  s.created_at AS summary_created_at
FROM ctx_item ci
LEFT JOIN ctx_message m ON m.id = ci.message_id
LEFT JOIN ctx_summary s ON s.id = ci.summary_id
WHERE ci.conversation_id = sqlc.arg('conversation_id')
ORDER BY ci.ordinal ASC
LIMIT NULLIF(sqlc.arg(limit_count), -1) OFFSET sqlc.arg(offset_count);

-- name: GetContextItemCount :one
SELECT COUNT(*) FROM ctx_item WHERE conversation_id = $1;

-- name: GetMaxContextOrdinal :one
SELECT CAST(COALESCE(MAX(ordinal), 0) AS BIGINT) FROM ctx_item WHERE conversation_id = $1;

-- name: DeleteContextItemsInRange :exec
DELETE FROM ctx_item
WHERE conversation_id = $1 AND ordinal >= $2 AND ordinal <= $3;

-- name: GetContextTokenCount :one
SELECT CAST(
    COALESCE(SUM(
        CASE
            WHEN ci.item_type = 'message' THEN m.token_count
            WHEN ci.item_type = 'summary' THEN s.token_count
            ELSE 0
        END
    ), 0)
AS BIGINT)
FROM ctx_item ci
LEFT JOIN ctx_message m ON ci.message_id = m.id
LEFT JOIN ctx_summary s ON ci.summary_id = s.id
WHERE ci.conversation_id = $1;

-- name: GetContextStats :one
SELECT
  (SELECT COUNT(*) FROM ctx_message cm WHERE cm.conversation_id = sqlc.arg('conversation_id')) AS message_count,
  (SELECT CAST(COALESCE(SUM(cm.token_count), 0) AS BIGINT) FROM ctx_message cm WHERE cm.conversation_id = sqlc.arg('conversation_id')) AS source_token_count,
	(SELECT COUNT(*) FROM ctx_summary cs WHERE cs.conversation_id = sqlc.arg('conversation_id')) AS summary_count,
  (SELECT CAST(COALESCE(SUM(
        CASE
            WHEN ci.item_type = 'message' THEN m.token_count
            WHEN ci.item_type = 'summary' THEN s.token_count
            ELSE 0
        END
    ), 0) AS BIGINT)
   FROM ctx_item ci
   LEFT JOIN ctx_message m ON ci.message_id = m.id
   LEFT JOIN ctx_summary s ON ci.summary_id = s.id
   WHERE ci.conversation_id = sqlc.arg('conversation_id')) AS active_token_count,
  (SELECT CAST(COALESCE(MAX(cs.depth), 0) AS BIGINT) FROM ctx_summary cs WHERE cs.conversation_id = sqlc.arg('conversation_id')) AS summary_depth;

-- name: GetContextMessageItems :many
SELECT ci.*, m.token_count as msg_token_count
FROM ctx_item ci
JOIN ctx_message m ON ci.message_id = m.id
WHERE ci.conversation_id = $1 AND ci.item_type = 'message'
ORDER BY ci.ordinal ASC;

-- name: GetFreshTailMessageIDs :many
SELECT ci.message_id FROM ctx_item ci
WHERE ci.conversation_id = $1 AND ci.item_type = 'message'
ORDER BY ci.ordinal DESC
LIMIT $2;

-- name: DeleteAllContextItems :exec
DELETE FROM ctx_item WHERE conversation_id = $1;
