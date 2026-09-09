-- name: IsNativeAgentDenied :one
SELECT EXISTS(
    SELECT 1
    FROM native_agent_deny
    WHERE native_id = $1 AND agent_id = $2
) AS denied;

-- name: GetNativeAdmission :one
SELECT
    COALESCE((SELECT p.enabled FROM plugin AS p WHERE p.id = $1), false)::boolean AS global_enabled,
    EXISTS(SELECT 1 FROM plugin AS p WHERE p.id = $1) AS global_present,
    EXISTS(
        SELECT 1 FROM native_agent_deny
        WHERE native_id = $1 AND agent_id = $2
    ) AS denied;

-- name: SetNativeAgentDeny :one
INSERT INTO native_agent_deny (native_id, agent_id)
VALUES ($1, $2)
ON CONFLICT (native_id, agent_id) DO NOTHING
RETURNING true AS inserted;

-- name: DeleteNativeAgentDeny :one
DELETE FROM native_agent_deny
WHERE native_id = $1 AND agent_id = $2
RETURNING true AS deleted;

-- name: ListNativeAgentDenials :many
SELECT native_id, agent_id, created_at, updated_at
FROM native_agent_deny
WHERE native_id = $1
ORDER BY agent_id;
