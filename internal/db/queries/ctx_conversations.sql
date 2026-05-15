-- name: CreateConversation :one
INSERT INTO ctx_conversations (id, session_id, title)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM ctx_conversations WHERE id = ?;

-- name: GetConversationBySessionID :one
SELECT * FROM ctx_conversations WHERE session_id = ?;

-- name: UpdateConversationTitle :exec
UPDATE ctx_conversations SET title = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateConversationBootstrapped :exec
UPDATE ctx_conversations SET bootstrapped_at = datetime('now'), updated_at = datetime('now') WHERE id = ?;

-- name: CreateConversationFull :one
INSERT INTO ctx_conversations (id, session_id, title, channel, archived, last_active, agent_id, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateConversationAgentUser :exec
UPDATE ctx_conversations SET agent_id = ?, user_id = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationArchived :exec
UPDATE ctx_conversations SET archived = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationLastActive :exec
UPDATE ctx_conversations SET last_active = datetime('now'), updated_at = datetime('now') WHERE session_id = ?;

-- name: UpdateConversationTitleBySessionID :exec
UPDATE ctx_conversations SET title = ?, updated_at = datetime('now') WHERE session_id = ?;

-- name: ListConversations :many
SELECT * FROM ctx_conversations WHERE archived = 0 ORDER BY last_active DESC;

-- name: ListConversationsAll :many
SELECT * FROM ctx_conversations ORDER BY last_active DESC;

