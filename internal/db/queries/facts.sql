-- name: InsertFact :one
INSERT INTO facts (id, subject, scope, user_id, agent_id, content, status, metadata, supersedes, version, source, created_at, updated_at)
VALUES ($1, $2, 'user_agent', $3, $4, $5, 'active', $6, $7, 1, $8, now(), now())
RETURNING *;

-- name: GetFact :one
SELECT * FROM facts WHERE id = $1;

-- name: UpdateFact :one
UPDATE facts
SET content = $2,
    metadata = $3,
    version = version + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeprecateFact :one
UPDATE facts
SET status = 'deprecated',
    version = version + 1,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListActiveFactsBySubject :many
SELECT * FROM facts
WHERE user_id = $1
  AND agent_id = $2
  AND subject = $3
  AND status = 'active'
ORDER BY created_at, id;

-- name: ListFactChangelogUpToVersion :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1
  AND agent_id = $2
  AND scope = 'fact'
  AND memory_version_after <= $3
ORDER BY memory_version_after ASC, id ASC;
