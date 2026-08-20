-- name: GetChannelIdentityName :one
SELECT name FROM channel_identity
WHERE platform = sqlc.arg(platform) AND external_id = sqlc.arg(external_id);
