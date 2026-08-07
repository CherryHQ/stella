-- name: CreateChannelGuest :one
WITH channel_lock AS (
    SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(channel_id), 0))
), capacity AS (
    SELECT 1
    FROM channel_lock
    WHERE (SELECT count(*) FROM channel_guest WHERE channel_guest.channel_id = sqlc.arg(channel_id)) < sqlc.arg(max_guests)::bigint
)
INSERT INTO channel_guest (id, channel_id, platform, external_id)
SELECT sqlc.arg(id), sqlc.arg(channel_id), sqlc.arg(platform), sqlc.arg(external_id)
FROM capacity
RETURNING *;

-- name: GetChannelGuest :one
SELECT * FROM channel_guest WHERE id = $1;

-- name: UpdateChannelGuestActivityByExternalID :one
UPDATE channel_guest
SET updated_at = now()
WHERE channel_id = $1 AND platform = $2 AND external_id = $3
RETURNING *;

-- name: DeleteChannelGuest :exec
DELETE FROM channel_guest WHERE id = $1;

-- name: PurgeExpiredChannelGuest :execrows
WITH channel_retention AS (
    SELECT id, CASE
        WHEN retention_value ~ '^[0-9]+$'
             AND retention_value::numeric BETWEEN 1 AND 365
            THEN retention_value::integer
        ELSE 30
    END AS retention_days
    FROM (
        SELECT id, (CASE
            WHEN pg_input_is_valid(config, 'jsonb') THEN config::jsonb
            ELSE '{}'::jsonb
        END)->>'guest_retention_days' AS retention_value
        FROM channel
    ) AS channel_config
)
DELETE FROM channel_guest AS guest
USING channel_retention
WHERE channel_retention.id = guest.channel_id
  AND guest.updated_at < now() - make_interval(days => channel_retention.retention_days);
