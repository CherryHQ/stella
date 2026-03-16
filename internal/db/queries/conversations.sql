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

-- name: CreateConversationFull :one
INSERT INTO conversations (session_id, title, channel, archived, last_active, agent_id, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateConversationAgentUser :exec
UPDATE conversations SET agent_id = ?, user_id = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationArchived :exec
UPDATE conversations SET archived = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationLastActive :exec
UPDATE conversations SET last_active = datetime('now'), updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationTitleBySessionID :exec
UPDATE conversations SET title = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: ListConversations :many
SELECT * FROM conversations WHERE archived = 0 ORDER BY last_active DESC;

-- name: ListConversationsAll :many
SELECT * FROM conversations ORDER BY last_active DESC;
