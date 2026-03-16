-- name: GetChatAgent :one
SELECT * FROM chat_agents WHERE platform = ? AND chat_id = ?;

-- name: UpsertChatAgent :exec
INSERT INTO chat_agents (platform, chat_id, agent_id, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(platform, chat_id) DO UPDATE SET
    agent_id = excluded.agent_id,
    updated_at = datetime('now');

-- name: DeleteChatAgent :exec
DELETE FROM chat_agents WHERE platform = ? AND chat_id = ?;

-- name: ListChatAgents :many
SELECT * FROM chat_agents ORDER BY platform, chat_id;
