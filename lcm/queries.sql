-- Conversations (Task 1.3)

-- name: CreateConversation :one
INSERT INTO conversations (session_id, title)
VALUES (?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = ?;

-- name: GetConversationBySessionID :one
SELECT * FROM conversations WHERE session_id = ?;

-- name: UpdateConversationTitle :exec
UPDATE conversations SET title = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateConversationBootstrapped :exec
UPDATE conversations SET bootstrapped_at = datetime('now'), updated_at = datetime('now') WHERE id = ?;

-- Messages (Task 1.4)

-- name: CreateMessage :one
INSERT INTO messages (conversation_id, seq, role, content, token_count)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMessage :one
SELECT * FROM messages WHERE id = ?;

-- name: GetMessagesByConversation :many
SELECT * FROM messages WHERE conversation_id = ? ORDER BY seq ASC;

-- name: GetMessagesByConversationRange :many
SELECT * FROM messages
WHERE conversation_id = ? AND seq >= ? AND seq <= ?
ORDER BY seq ASC;

-- name: GetMessageCount :one
SELECT COUNT(*) FROM messages WHERE conversation_id = ?;

-- name: GetMaxSeq :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) FROM messages WHERE conversation_id = ?;

-- name: CreateMessagePart :exec
INSERT INTO message_parts (id, message_id, part_type, ordinal, text_content, tool_call_id, tool_name, tool_input, tool_output, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetMessageParts :many
SELECT * FROM message_parts WHERE message_id = ? ORDER BY ordinal ASC;

-- name: GetMessagePartsByMessages :many
SELECT * FROM message_parts WHERE message_id IN (sqlc.slice('message_ids')) ORDER BY message_id, ordinal ASC;

-- Summaries (Task 1.5)

-- name: CreateSummary :exec
INSERT INTO summaries (id, conversation_id, kind, depth, content, token_count, earliest_at, latest_at, descendant_count, descendant_token_count, source_message_token_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSummary :one
SELECT * FROM summaries WHERE id = ?;

-- name: GetSummariesByConversation :many
SELECT * FROM summaries WHERE conversation_id = ? ORDER BY created_at ASC;

-- name: GetSummariesByDepth :many
SELECT * FROM summaries
WHERE conversation_id = ? AND depth = ?
ORDER BY created_at ASC;

-- name: LinkSummaryToMessage :exec
INSERT INTO summary_messages (summary_id, message_id, ordinal)
VALUES (?, ?, ?);

-- name: LinkSummaryToParent :exec
INSERT INTO summary_parents (summary_id, parent_summary_id, ordinal)
VALUES (?, ?, ?);

-- name: GetSummaryMessages :many
SELECT m.* FROM messages m
JOIN summary_messages sm ON sm.message_id = m.id
WHERE sm.summary_id = ?
ORDER BY sm.ordinal ASC;

-- name: GetSummaryParents :many
SELECT s.* FROM summaries s
JOIN summary_parents sp ON sp.parent_summary_id = s.id
WHERE sp.summary_id = ?
ORDER BY sp.ordinal ASC;

-- name: GetSummaryChildren :many
SELECT s.* FROM summaries s
JOIN summary_parents sp ON sp.summary_id = s.id
WHERE sp.parent_summary_id = ?
ORDER BY sp.ordinal ASC;

-- name: SearchMessages :many
SELECT * FROM messages
WHERE conversation_id = ? AND content LIKE ?
ORDER BY seq ASC
LIMIT ?;

-- name: SearchSummaries :many
SELECT * FROM summaries
WHERE conversation_id = ? AND content LIKE ?
ORDER BY created_at ASC
LIMIT ?;

-- Context Items (Task 1.6)

-- name: AppendContextItem :exec
INSERT INTO context_items (conversation_id, ordinal, item_type, message_id, summary_id)
VALUES (?, ?, ?, ?, ?);

-- name: GetContextItems :many
SELECT * FROM context_items
WHERE conversation_id = ?
ORDER BY ordinal ASC;

-- name: GetContextItemCount :one
SELECT COUNT(*) FROM context_items WHERE conversation_id = ?;

-- name: GetMaxContextOrdinal :one
SELECT CAST(COALESCE(MAX(ordinal), 0) AS INTEGER) FROM context_items WHERE conversation_id = ?;

-- name: DeleteContextItemsInRange :exec
DELETE FROM context_items
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
FROM context_items ci
LEFT JOIN messages m ON ci.message_id = m.id
LEFT JOIN summaries s ON ci.summary_id = s.id
WHERE ci.conversation_id = ?;

-- name: GetContextMessageItems :many
SELECT ci.*, m.token_count as msg_token_count
FROM context_items ci
JOIN messages m ON ci.message_id = m.id
WHERE ci.conversation_id = ? AND ci.item_type = 'message'
ORDER BY ci.ordinal ASC;

-- name: GetFreshTailMessageIDs :many
SELECT ci.message_id FROM context_items ci
WHERE ci.conversation_id = ? AND ci.item_type = 'message'
ORDER BY ci.ordinal DESC
LIMIT ?;

-- name: DeleteAllContextItems :exec
DELETE FROM context_items WHERE conversation_id = ?;
