-- name: CreateChannelGroupRoute :one
-- One route row is the durable classification fact for one canonical group
-- sequence. The insert is idempotent so a re-delivered outbox cannot mint a
-- second sequence authority.
INSERT INTO channel_group_route (id, group_message_id, group_id, group_seq)
VALUES ($1, $2, $3, $4)
ON CONFLICT (group_message_id) DO UPDATE
SET updated_at = channel_group_route.updated_at
RETURNING *;

-- name: GetChannelGroupRouteByMessage :one
SELECT *
FROM channel_group_route
WHERE group_message_id = $1;

-- name: ClaimChannelGroupRoute :one
-- A route claim is a sequence lease, not an execution lease. Only the oldest
-- non-completed route in a group can win; a stale claimant loses on the token
-- and expiry CAS in CompleteChannelGroupRoute.
UPDATE channel_group_route route
SET status = 'claimed',
    claim_token = sqlc.arg(claim_token),
    claim_expires_at = now() + make_interval(secs => sqlc.arg(lease_seconds)::integer),
    updated_at = now()
WHERE route.id = sqlc.arg(id)
  AND (
      route.status = 'pending'
      OR (route.status = 'claimed' AND route.claim_expires_at <= now())
  )
  AND NOT EXISTS (
      SELECT 1
      FROM channel_group_route earlier
      WHERE earlier.group_id = route.group_id
        AND earlier.group_seq < route.group_seq
        AND earlier.status <> 'completed'
  )
RETURNING *;

-- name: CompleteChannelGroupRoute :execrows
-- Decisions and responder FIFO rows are committed by the same transaction as
-- this CAS. A stale worker therefore cannot materialize responders after a
-- newer worker has reclaimed the route.
UPDATE channel_group_route
SET status = 'completed',
    decisions = sqlc.arg(decisions),
    completed_at = now(),
    claim_token = NULL,
    claim_expires_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'claimed'
  AND claim_token = sqlc.arg(claim_token)
  AND claim_expires_at > now();
