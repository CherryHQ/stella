-- name: GetChatAgent :one
SELECT * FROM channel_agent WHERE channel_id = $1 AND platform = $2 AND chat_id = $3;

-- name: UpsertChatAgent :exec
INSERT INTO channel_agent (channel_id, platform, chat_id, agent_id, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT(channel_id, platform, chat_id) DO UPDATE SET
    agent_id = excluded.agent_id,
    updated_at = now();

-- name: DeleteChatAgent :exec
DELETE FROM channel_agent WHERE channel_id = $1 AND platform = $2 AND chat_id = $3;

-- name: ListChatAgents :many
SELECT * FROM channel_agent ORDER BY channel_id, platform, chat_id;
