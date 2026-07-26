-- name: CreateSessionRotationNonce :one
INSERT INTO agent_session_rotation_nonce (
    id, session_id, binding_key, actor_id, turn_marker, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetSessionRotationNonce :one
SELECT * FROM agent_session_rotation_nonce WHERE id = $1;

-- name: ClaimSessionRotationNonce :one
-- Single-use is enforced here, not in Go: the guard and the write are one
-- statement, so two confirmations racing across nodes cannot both rotate.
-- Claiming deletes rather than marks: a spent nonce authorizes nothing, so
-- keeping it would only leave residue the sweep below has to carry.
DELETE FROM agent_session_rotation_nonce
WHERE id = $1
  AND used_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredSessionRotationNonce :exec
-- Bounds the table without a background sweeper: every issued nonce either gets
-- claimed (deleted above) or expires, and each new nonce pays for one global
-- sweep of the expired ones. The predicate is served by
-- idx_agent_session_rotation_nonce_expires_at.
DELETE FROM agent_session_rotation_nonce
WHERE expires_at < now();
