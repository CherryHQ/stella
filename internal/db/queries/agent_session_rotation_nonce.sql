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
UPDATE agent_session_rotation_nonce
SET used_at = now(), updated_at = now()
WHERE id = $1
  AND used_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredSessionRotationNonceForBinding :exec
DELETE FROM agent_session_rotation_nonce
WHERE binding_key = $1
  AND (expires_at <= now() OR used_at IS NOT NULL);
