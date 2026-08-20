-- name: ListGroupMembers :many
SELECT * FROM channel_group_member WHERE group_id = $1 ORDER BY agent_id;

-- name: AddGroupMember :one
INSERT INTO channel_group_member (group_id, agent_id, reply_channel_id)
VALUES ($1, $2, $3)
ON CONFLICT(group_id, agent_id) DO UPDATE SET
    reply_channel_id = excluded.reply_channel_id,
    updated_at = now()
RETURNING *;

-- name: RemoveGroupMember :exec
DELETE FROM channel_group_member WHERE group_id = $1 AND agent_id = $2;

-- name: CountGroupMembers :one
SELECT COUNT(*) FROM channel_group_member WHERE group_id = $1;
