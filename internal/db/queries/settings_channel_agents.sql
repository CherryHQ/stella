-- name: GetChatAgent :one
SELECT * FROM settings_channel_agents WHERE platform = ? AND chat_id = ?;

-- name: UpsertChatAgent :exec
INSERT INTO settings_channel_agents (platform, chat_id, agent_id, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(platform, chat_id) DO UPDATE SET
    agent_id = excluded.agent_id,
    updated_at = datetime('now');

-- name: DeleteChatAgent :exec
DELETE FROM settings_channel_agents WHERE platform = ? AND chat_id = ?;

-- name: ListChatAgents :many
SELECT * FROM settings_channel_agents ORDER BY platform, chat_id;
