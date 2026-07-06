-- name: CreateVaultExecSecretAudit :one
INSERT INTO vault_exec_secret_audit (id, user_id, agent_id, session_id, name, command_text)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListVaultExecSecretAuditByUser :many
SELECT *
FROM vault_exec_secret_audit
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2;
