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
