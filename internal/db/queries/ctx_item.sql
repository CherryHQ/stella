-- name: AppendContextItem :exec
INSERT INTO ctx_item (conversation_id, ordinal, item_type, message_id, summary_id, event_type)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetContextItems :many
SELECT * FROM ctx_item
WHERE conversation_id = ?
ORDER BY ordinal ASC;

-- name: GetContextItemCount :one
SELECT COUNT(*) FROM ctx_item WHERE conversation_id = ?;

-- name: GetMaxContextOrdinal :one
SELECT CAST(COALESCE(MAX(ordinal), 0) AS INTEGER) FROM ctx_item WHERE conversation_id = ?;

-- name: DeleteContextItemsInRange :exec
DELETE FROM ctx_item
WHERE conversation_id = ? AND ordinal >= ? AND ordinal <= ?;

-- name: GetContextTokenCount :one
SELECT CAST(
    COALESCE(SUM(
        CASE
            WHEN ci.item_type = 'message' THEN m.token_count
            WHEN ci.item_type = 'summary' THEN s.token_count
            ELSE 0
        END
    ), 0)
AS INTEGER)
FROM ctx_item ci
LEFT JOIN ctx_message m ON ci.message_id = m.id
LEFT JOIN ctx_summary s ON ci.summary_id = s.id
WHERE ci.conversation_id = ?;

-- name: GetContextMessageItems :many
SELECT ci.*, m.token_count as msg_token_count
FROM ctx_item ci
JOIN ctx_message m ON ci.message_id = m.id
WHERE ci.conversation_id = ? AND ci.item_type = 'message'
ORDER BY ci.ordinal ASC;

-- name: GetFreshTailMessageIDs :many
SELECT ci.message_id FROM ctx_item ci
WHERE ci.conversation_id = ? AND ci.item_type = 'message'
ORDER BY ci.ordinal DESC
LIMIT ?;

-- name: DeleteAllContextItems :exec
DELETE FROM ctx_item WHERE conversation_id = ?;
