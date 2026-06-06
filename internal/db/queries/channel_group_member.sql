-- name: ListGroupMembers :many
SELECT * FROM channel_group_member WHERE group_id = ?;

-- name: GetGroupMember :one
SELECT * FROM channel_group_member WHERE group_id = ? AND agent_id = ?;

-- name: AddGroupMember :one
INSERT INTO channel_group_member (group_id, agent_id, reply_channel_id)
VALUES (?, ?, ?)
ON CONFLICT(group_id, agent_id) DO UPDATE SET
    reply_channel_id = excluded.reply_channel_id,
    updated_at = datetime('now')
RETURNING *;

-- name: RemoveGroupMember :exec
DELETE FROM channel_group_member WHERE group_id = ? AND agent_id = ?;

-- name: CountGroupMembers :one
SELECT COUNT(*) FROM channel_group_member WHERE group_id = ?;

-- name: ListGroupsByAgent :many
SELECT * FROM channel_group_member WHERE agent_id = ?;

-- name: ListGroupsByReplyChannel :many
SELECT * FROM channel_group_member WHERE reply_channel_id = ?;
