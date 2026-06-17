-- name: InsertMemoryChangelog :exec
INSERT INTO ctx_agent_memory_changelog (id, user_id, agent_id, session_id, entity_id, scope, action, source, memory_version_before, memory_version_after, before_text, after_text, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListMemoryChangelog :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1 AND agent_id = $2 AND scope = $3
ORDER BY id DESC
LIMIT $4;

-- name: GetMemoryChangelogAtVersion :one
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1 AND agent_id = $2 AND scope = $3 AND memory_version_after <= $4
ORDER BY memory_version_after DESC, id DESC
LIMIT 1;
