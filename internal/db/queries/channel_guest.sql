-- name: CreateChannelGuest :one
INSERT INTO channel_guest (id, channel_id, platform, external_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetChannelGuest :one
SELECT * FROM channel_guest WHERE id = $1;

-- name: GetChannelGuestByExternalID :one
SELECT * FROM channel_guest
WHERE channel_id = $1 AND platform = $2 AND external_id = $3;

-- name: DeleteChannelGuest :exec
DELETE FROM channel_guest WHERE id = $1;
