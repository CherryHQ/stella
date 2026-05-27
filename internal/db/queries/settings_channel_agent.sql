-- name: GetChatAgent :one
SELECT * FROM settings_channel_agent WHERE channel_id = ? AND platform = ? AND chat_id = ? AND org_id = ?;

-- name: UpsertChatAgent :exec
INSERT INTO settings_channel_agent (channel_id, platform, chat_id, agent_id, org_id, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(channel_id, platform, chat_id, org_id) DO UPDATE SET
    agent_id = excluded.agent_id,
    updated_at = datetime('now');

-- name: DeleteChatAgent :exec
DELETE FROM settings_channel_agent WHERE channel_id = ? AND platform = ? AND chat_id = ? AND org_id = ?;

-- name: ListChatAgents :many
SELECT * FROM settings_channel_agent WHERE org_id = ? ORDER BY channel_id, platform, chat_id;
