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

-- name: SearchSummaries :many
SELECT * FROM summaries
WHERE conversation_id = ? AND content LIKE ?
ORDER BY created_at ASC
LIMIT ?;
