-- name: CreateMessage :one
INSERT INTO ctx_message (id, conversation_id, seq, role, event_type, content, token_count)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMessage :one
SELECT * FROM ctx_message WHERE id = ? AND conversation_id = ?;

-- name: GetMessagesByConversation :many
SELECT * FROM ctx_message WHERE conversation_id = ? ORDER BY seq ASC;

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

-- name: GetMessageParts :many
SELECT * FROM ctx_message_part WHERE message_id = ? ORDER BY ordinal ASC;

-- name: GetMessagePartsByMessages :many
SELECT * FROM ctx_message_part WHERE message_id IN (sqlc.slice('message_ids')) ORDER BY message_id, ordinal ASC;

-- name: SearchMessages :many
SELECT * FROM ctx_message
WHERE conversation_id = ? AND content LIKE ?
ORDER BY seq ASC
LIMIT ?;

-- name: GetMessagesSince :many
SELECT * FROM ctx_message
WHERE conversation_id = ? AND created_at > ?
ORDER BY seq ASC;
