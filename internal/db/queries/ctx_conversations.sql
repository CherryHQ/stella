-- name: CreateConversation :one
INSERT INTO ctx_conversations (id, session_id, title, channel, kind, project_id, archived, last_active, agent_id, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetConversation :one
SELECT * FROM ctx_conversations
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id);

-- name: GetConversationBySessionID :one
SELECT * FROM ctx_conversations
WHERE session_id = sqlc.arg(session_id)
  AND user_id = sqlc.arg(user_id);

-- name: GetUnownedConversationBySessionID :one
SELECT * FROM ctx_conversations
WHERE session_id = sqlc.arg(session_id)
  AND user_id IS NULL;

-- name: ClaimConversationUserBySessionID :exec
UPDATE ctx_conversations
SET user_id = sqlc.arg(user_id), updated_at = datetime('now')
WHERE session_id = sqlc.arg(session_id)
  AND user_id IS NULL;

-- name: UpdateConversationTitle :exec
UPDATE ctx_conversations SET title = sqlc.arg(title), updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: UpdateConversationBootstrapped :exec
UPDATE ctx_conversations SET bootstrapped_at = datetime('now'), updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: UpdateConversationArchived :exec
UPDATE ctx_conversations SET archived = sqlc.arg(archived), updated_at = datetime('now')
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id);

-- name: UpdateConversationLastActive :exec
UPDATE ctx_conversations SET last_active = datetime('now'), updated_at = datetime('now')
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id);

-- name: UpdateConversationTitleBySessionID :exec
UPDATE ctx_conversations SET title = sqlc.arg(title), updated_at = datetime('now')
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id);

-- name: ListConversations :many
SELECT * FROM ctx_conversations
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id) IS NULL OR agent_id = sqlc.narg(agent_id))
  AND archived = 0
ORDER BY last_active DESC;

-- name: ListConversationsAll :many
SELECT * FROM ctx_conversations
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(agent_id) IS NULL OR agent_id = sqlc.narg(agent_id))
ORDER BY last_active DESC;

-- name: ListConversationsByKind :many
SELECT * FROM ctx_conversations WHERE agent_id = ? AND user_id = ? AND kind = ? AND archived = 0 ORDER BY last_active DESC;

-- name: GetMainConversationByProject :one
SELECT * FROM ctx_conversations
WHERE project_id = sqlc.arg(project_id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND kind = 'main' AND archived = 0 LIMIT 1;

-- name: UpdateConversationKindProject :exec
UPDATE ctx_conversations SET kind = sqlc.arg(kind), project_id = sqlc.arg(project_id), updated_at = datetime('now')
WHERE session_id = sqlc.arg(session_id) AND user_id = sqlc.arg(user_id);
