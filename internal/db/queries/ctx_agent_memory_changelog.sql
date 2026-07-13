-- name: InsertMemoryChangelog :exec
INSERT INTO ctx_agent_memory_changelog (id, user_id, agent_id, session_id, entity_id, scope, action, source, memory_version_before, memory_version_after, before_text, after_text, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListMemoryChangelog :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1 AND agent_id = $2 AND scope = $3
ORDER BY id DESC
LIMIT $4;

-- name: ListMemoryChangelogPage :many
SELECT *
FROM ctx_agent_memory_changelog
WHERE user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND scope = sqlc.arg(scope)
  AND (
    (sqlc.narg(cursor_created_at)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (created_at, id::text) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::text)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: GetMemoryChangelogAtVersion :one
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1 AND agent_id = $2 AND scope = $3 AND memory_version_after <= $4
ORDER BY memory_version_after DESC, id DESC
LIMIT 1;
