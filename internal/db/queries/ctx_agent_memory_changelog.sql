-- name: InsertMemoryChangelog :exec
INSERT INTO ctx_agent_memory_changelog (id, user_id, agent_id, session_id, entity_id, scope, action, source, memory_version_before, memory_version_after, before_text, after_text, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListMemoryChangelog :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = ? AND agent_id = ? AND scope = ?
ORDER BY id DESC
LIMIT ?;

-- name: GetMemoryChangelogAtVersion :one
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = ? AND agent_id = ? AND scope = ? AND memory_version_after <= ?
ORDER BY memory_version_after DESC, id DESC
LIMIT 1;
