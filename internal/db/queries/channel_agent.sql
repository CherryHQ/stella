-- name: GetChatAgent :one
SELECT * FROM channel_agent WHERE channel_id = ? AND platform = ? AND chat_id = ?;

-- name: UpsertChatAgent :exec
INSERT INTO channel_agent (channel_id, platform, chat_id, agent_id, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(channel_id, platform, chat_id) DO UPDATE SET
    agent_id = excluded.agent_id,
    updated_at = datetime('now');

-- name: DeleteChatAgent :exec
DELETE FROM channel_agent WHERE channel_id = ? AND platform = ? AND chat_id = ?;

-- name: ListChatAgents :many
SELECT * FROM channel_agent ORDER BY channel_id, platform, chat_id;
