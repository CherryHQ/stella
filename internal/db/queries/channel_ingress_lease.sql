-- name: GetChannelIngressLease :one
-- Read the singleton lease row (observability + tests).
SELECT * FROM channel_ingress_lease WHERE id = 1;

-- name: AcquireChannelIngressLease :one
-- Atomic acquire-or-renew CAS against the single lease row. Succeeds (returns a
-- row) when this replica already holds the lease, when the lease is free, or when
-- the current holder's lease has expired; returns no rows when another replica
-- holds a live lease. acquired_at is preserved across renewals by the same owner
-- and reset to now() on a fresh acquisition. lease_expires_at is computed from the
-- DATABASE clock so expiry is comparable across replicas regardless of local skew.
UPDATE channel_ingress_lease
SET owner_id = sqlc.arg(owner_id),
    acquired_at = CASE
        WHEN owner_id = sqlc.arg(owner_id) THEN acquired_at
        ELSE now()
    END,
    heartbeat_at = now(),
    lease_expires_at = now() + make_interval(secs => sqlc.arg(ttl_seconds)::double precision),
    updated_at = now()
WHERE id = 1
  AND (
        owner_id = sqlc.arg(owner_id)
     OR owner_id = ''
     OR lease_expires_at IS NULL
     OR lease_expires_at < now()
  )
RETURNING owner_id, lease_expires_at;

-- name: ReleaseChannelIngressLease :execrows
-- Graceful release on shutdown: frees the lease so a peer can acquire immediately
-- (no waiting for expiry). Guarded by owner_id so a replica can only release a
-- lease it still holds — a stale ex-holder cannot free a lease a peer has taken.
UPDATE channel_ingress_lease
SET owner_id = '',
    lease_expires_at = now(),
    updated_at = now()
WHERE id = 1 AND owner_id = sqlc.arg(owner_id);
