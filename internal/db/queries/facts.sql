-- name: InsertFact :one
INSERT INTO facts (id, subject, scope, user_id, agent_id, content, status, metadata, supersedes, version, source, created_at, updated_at)
VALUES ($1, $2, 'user_agent', $3, $4, $5, 'active', $6, $7, 1, $8, now(), now())
RETURNING *;

-- name: GetFact :one
SELECT * FROM facts
WHERE id = $1
  AND user_id = $2
  AND agent_id = $3;

-- name: GetFactForUpdate :one
SELECT * FROM facts
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
FOR UPDATE;

-- name: DeprecateFact :one
UPDATE facts
SET status = 'deprecated',
    version = version + 1,
    updated_at = now()
WHERE id = $1
  AND user_id = $2
  AND agent_id = $3
RETURNING *;

-- name: RestoreKnowledgeFact :one
UPDATE facts
SET status = 'active',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND scope = 'user_agent'
  AND subject = 'world'
  AND status = 'deprecated'
RETURNING *;

-- name: ListActiveFactsBySubject :many
SELECT * FROM facts
WHERE user_id = $1
  AND agent_id = $2
  AND subject = $3
  AND status = 'active'
ORDER BY created_at, id;

-- name: ListActiveKnowledge :many
SELECT *
FROM facts
WHERE user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND scope = 'user_agent'
  AND subject = 'world'
  AND status = 'active'
  AND (
    (sqlc.narg(cursor_timestamp)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (updated_at, id::text) < (sqlc.narg(cursor_timestamp)::timestamptz, sqlc.narg(cursor_id)::text)
  )
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: CountActiveKnowledge :one
SELECT count(*)
FROM facts
WHERE user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND scope = 'user_agent'
  AND subject = 'world'
  AND status = 'active';

-- name: ListRemovedKnowledge :many
SELECT
  f.*,
  d.created_at AS deprecated_at,
  d.metadata AS deprecate_metadata
FROM facts f
JOIN LATERAL (
  SELECT c.created_at, c.metadata
  FROM ctx_agent_memory_changelog c
  WHERE c.user_id = f.user_id
    AND c.agent_id = f.agent_id
    AND c.scope = 'fact'
    AND c.action = 'deprecate'
    AND c.entity_id = f.id::text
  ORDER BY c.created_at DESC, c.id DESC
  LIMIT 1
) d ON true
WHERE f.user_id = sqlc.arg(user_id)
  AND f.agent_id = sqlc.arg(agent_id)
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'deprecated'
  AND (
    (d.metadata::jsonb)->>'deprecated_by' = 'manual'
    OR (d.metadata::jsonb)->>'curator' = 'usage'
  )
  AND d.created_at > sqlc.arg(now_at)::timestamptz - interval '2160 hours'
  AND (
    (sqlc.narg(cursor_timestamp)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (d.created_at, f.id::text) < (sqlc.narg(cursor_timestamp)::timestamptz, sqlc.narg(cursor_id)::text)
  )
ORDER BY d.created_at DESC, f.id DESC
LIMIT sqlc.arg(limit_count);

-- name: CountRemovedKnowledge :one
SELECT count(*)
FROM facts f
JOIN LATERAL (
  SELECT c.created_at, c.metadata
  FROM ctx_agent_memory_changelog c
  WHERE c.user_id = f.user_id
    AND c.agent_id = f.agent_id
    AND c.scope = 'fact'
    AND c.action = 'deprecate'
    AND c.entity_id = f.id::text
  ORDER BY c.created_at DESC, c.id DESC
  LIMIT 1
) d ON true
WHERE f.user_id = sqlc.arg(user_id)
  AND f.agent_id = sqlc.arg(agent_id)
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'deprecated'
  AND (
    (d.metadata::jsonb)->>'deprecated_by' = 'manual'
    OR (d.metadata::jsonb)->>'curator' = 'usage'
  )
  AND d.created_at > sqlc.arg(now_at)::timestamptz - interval '2160 hours';

-- name: GetLatestQualifyingKnowledgeDeprecateChangelog :one
SELECT *
FROM (
  SELECT *
  FROM ctx_agent_memory_changelog
  WHERE user_id = sqlc.arg(user_id)
    AND agent_id = sqlc.arg(agent_id)
    AND scope = 'fact'
    AND action = 'deprecate'
    AND entity_id = sqlc.arg(fact_id)::text
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest
WHERE (latest.metadata::jsonb)->>'deprecated_by' = 'manual'
   OR (latest.metadata::jsonb)->>'curator' = 'usage';

-- name: ListActiveKnowledgeContents :many
SELECT content
FROM facts
WHERE user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND scope = 'user_agent'
  AND subject = 'world'
  AND status = 'active';

-- name: ListFactChangelogUpToVersion :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1
  AND agent_id = $2
  AND scope = 'fact'
  AND memory_version_after <= $3
ORDER BY memory_version_after ASC, id ASC;

-- name: ListFactChangelogBySubject :many
SELECT * FROM ctx_agent_memory_changelog
WHERE user_id = $1
  AND agent_id = $2
  AND scope = 'fact'
  AND (
    (before_text IS NOT NULL AND before_text::jsonb->>'subject' = sqlc.arg(subject))
    OR (after_text IS NOT NULL AND after_text::jsonb->>'subject' = sqlc.arg(subject))
  )
ORDER BY memory_version_after DESC NULLS LAST, id DESC
LIMIT sqlc.arg(limit_count);

-- name: ListFactChangelogBySubjectPage :many
WITH subject_rows AS (
  SELECT *
  FROM ctx_agent_memory_changelog
  WHERE user_id = sqlc.arg(user_id)
    AND agent_id = sqlc.arg(agent_id)
    AND scope = 'fact'
    AND memory_version_after IS NOT NULL
    AND (
      (before_text IS NOT NULL AND before_text::jsonb->>'subject' = sqlc.arg(subject))
      OR (after_text IS NOT NULL AND after_text::jsonb->>'subject' = sqlc.arg(subject))
    )
), group_keys AS (
  SELECT DISTINCT ON (memory_version_after)
    memory_version_after,
    created_at AS key_created_at,
    id AS key_id
  FROM subject_rows
  ORDER BY memory_version_after, created_at DESC, id DESC
), selected_groups AS (
  SELECT *
  FROM group_keys
  WHERE (
    (sqlc.narg(cursor_created_at)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (key_created_at, key_id::text) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::text)
  )
  ORDER BY key_created_at DESC, key_id DESC
  LIMIT sqlc.arg(limit_count)
)
SELECT r.*
FROM subject_rows r
JOIN selected_groups g USING (memory_version_after)
ORDER BY g.key_created_at DESC, g.key_id DESC, r.created_at DESC, r.id DESC;
