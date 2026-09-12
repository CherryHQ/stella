-- name: GetChannel :one
SELECT * FROM channel WHERE id = $1;

-- name: GetChannelForUpdate :one
SELECT * FROM channel WHERE id = $1 FOR UPDATE;

-- name: CreateChannel :one
INSERT INTO channel (id, name, type, agent_id, enabled, config)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateChannel :one
-- Config writes bump config_revision so a running owner can tell its
-- applied_revision is stale. Runtime fields go through the separate
-- lease queries below and are never touched here.
UPDATE channel
SET name = $2,
    type = $3,
    agent_id = $4,
    enabled = $5,
    config = $6,
    config_revision = config_revision + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CreateWebChannelIfNotExists :exec
INSERT INTO channel (id, name, type, agent_id)
VALUES ($1, 'Web', 'web', $2)
ON CONFLICT(id) DO NOTHING;

-- name: ListChannels :many
SELECT * FROM channel ORDER BY type, id;

-- name: ListChannelsByType :many
SELECT * FROM channel WHERE type = $1 ORDER BY id;

-- name: DeleteChannel :exec
DELETE FROM channel WHERE id = $1;

-- name: ListClaimableChannels :many
-- Replica scan: enabled channels with no live owner lease.
SELECT * FROM channel
WHERE enabled
  AND (runtime_lease_until IS NULL OR runtime_lease_until <= clock_timestamp())
ORDER BY id
LIMIT 50
FOR UPDATE SKIP LOCKED;

-- name: ClaimChannelRuntime :one
-- Acquire or extend ownership. Empty result = another replica holds a live lease.
UPDATE channel
SET runtime_owner_id = sqlc.arg(owner_id),
    runtime_token = sqlc.arg(token),
    runtime_lease_until = clock_timestamp() + interval '30 seconds',
    runtime_observed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(channel_id)
  AND (runtime_lease_until IS NULL OR runtime_lease_until <= clock_timestamp()
       OR runtime_token = sqlc.arg(token))
RETURNING *;

-- name: RenewChannelRuntime :one
-- Returns the row so the owner can compare desired config_revision/enabled
-- with what it applied; empty result = lease already lost.
UPDATE channel
SET runtime_lease_until = clock_timestamp() + interval '30 seconds',
    runtime_observed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(channel_id) AND runtime_token = sqlc.arg(token)
  AND runtime_lease_until > clock_timestamp()
RETURNING *;

-- name: ReleaseChannelRuntime :execrows
UPDATE channel
SET runtime_owner_id = NULL, runtime_token = NULL, runtime_lease_until = NULL,
    runtime_state = 'stopped', runtime_observed_at = clock_timestamp(),
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(channel_id) AND runtime_token = sqlc.arg(token);

-- name: UpdateChannelRuntimeState :execrows
-- Owner heartbeat: observed state + the config revision it runs, fenced by token.
UPDATE channel
SET runtime_state = sqlc.arg(state), runtime_error_code = sqlc.arg(error_code),
    applied_revision = sqlc.arg(applied_revision),
    runtime_observed_at = clock_timestamp(), updated_at = clock_timestamp()
WHERE id = sqlc.arg(channel_id) AND runtime_token = sqlc.arg(token)
  AND runtime_lease_until > clock_timestamp();

-- name: UpdateChannelCheckpoint :execrows
-- Durable ingress cursor, fenced by the owner's token.
UPDATE channel
SET receive_checkpoint = sqlc.arg(checkpoint), updated_at = clock_timestamp()
WHERE id = sqlc.arg(channel_id) AND runtime_token = sqlc.arg(token)
  AND runtime_lease_until > clock_timestamp();

-- name: ChannelSendAdmission :one
-- The channel's current owner token must match and be unexpired and enabled —
-- checked inside the outbox claim tx and again before every SDK call, so a
-- fenced-out owner never performs a send.
SELECT EXISTS(
  SELECT 1 FROM channel
  WHERE id = $1 AND runtime_token = $2
    AND runtime_lease_until > clock_timestamp() AND enabled
) AS ok;

-- name: RegisterChannelRuntimeAccount :execrows
-- The lease owner's adapter reports the platform account identity this channel
-- speaks as. Fenced by the replica-held runtime token so a fenced-out adapter
-- cannot overwrite the live binding; never cleared on release — a stale
-- last-known key safely rejects ops enqueued before a rebind.
UPDATE channel
SET runtime_account_key = sqlc.arg(account_key)
WHERE id = sqlc.arg(id)
  AND type = sqlc.arg(channel_type)
  AND runtime_token = sqlc.arg(runtime_token)::uuid
  AND runtime_lease_until > clock_timestamp();
