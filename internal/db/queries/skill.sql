-- name: CreateSkill :one
INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetSkill :one
SELECT * FROM skill
WHERE id = sqlc.arg(id)
  AND ((sqlc.narg(agent_id)::text IS NULL AND sqlc.narg(user_id)::uuid IS NULL)
    OR scope='system'
    OR (scope='system_agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user'         AND user_id=sqlc.narg(user_id))
    OR (scope='user_agent'   AND user_id=sqlc.narg(user_id) AND agent_id=sqlc.narg(agent_id)));

-- name: GetSkillByID :one
SELECT * FROM skill WHERE id = $1;

-- name: GetSkillForUpdate :one
SELECT * FROM skill WHERE id = $1 FOR UPDATE;

-- ListSkillsVisible returns the effective skill set for a (user, agent) context,
-- ordered most-specific-first so a name-dedup keeps the highest-precedence skill:
-- user_agent > user > system_agent > system.
-- name: ListSkillsVisible :many
SELECT * FROM skill
WHERE status != 'deprecated'
  AND disable_model_invocation = false
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id))
    OR (scope = 'user'         AND user_id = sqlc.narg(user_id))
    OR (scope = 'user_agent'   AND user_id = sqlc.narg(user_id) AND agent_id = sqlc.narg(agent_id))
  )
ORDER BY CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END, created_at;

-- ListSkillsForAgentContext returns the visible skills for one (user, agent),
-- ordered most-specific-first so a name-dedup downstream keeps the effective
-- skill: user_agent > user > system_agent > system.
-- name: ListSkillsForAgentContext :many
SELECT * FROM skill
WHERE status != 'deprecated'
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND agent_id = sqlc.arg(agent_id))
    OR (scope = 'user'         AND user_id = sqlc.arg(user_id))
    OR (scope = 'user_agent'   AND user_id = sqlc.arg(user_id) AND agent_id = sqlc.arg(agent_id))
  )
ORDER BY CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END, created_at;

-- name: ListSkillsByScope :many
SELECT * FROM skill
WHERE scope = sqlc.arg(scope)
  AND status != 'deprecated'
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
ORDER BY created_at;

-- name: ListSkillsForAdmin :many
SELECT * FROM skill
WHERE scope NOT IN ('user', 'user_agent')
      OR user_id = sqlc.arg(user_id)
ORDER BY scope, created_at;

-- name: ListSkillsForUser :many
SELECT * FROM skill
WHERE status != 'deprecated'
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND strpos(',' || sqlc.arg(agent_ids_csv) || ',', ',' || agent_id || ',') > 0)
    OR (scope = 'user' AND user_id = sqlc.arg(user_id))
    OR (scope = 'user_agent' AND user_id = sqlc.arg(user_id)
        AND strpos(',' || sqlc.arg(agent_ids_csv) || ',', ',' || agent_id || ',') > 0)
  )
ORDER BY scope, created_at;

-- ResolveSkill returns the single effective skill for a name in a (user, agent)
-- context. Precedence: user_agent > user > system_agent > system.
-- name: ResolveSkill :one
SELECT * FROM skill
WHERE name = sqlc.arg(name)
  AND status != 'deprecated'
  AND disable_model_invocation = false
  AND (
    scope = 'system'
    OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id))
    OR (scope = 'user'         AND user_id = sqlc.narg(user_id))
    OR (scope = 'user_agent'   AND user_id = sqlc.narg(user_id) AND agent_id = sqlc.narg(agent_id))
  )
ORDER BY
  CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END ASC
LIMIT 1;

-- name: UpdateSkillMetadata :exec
UPDATE skill
SET description              = sqlc.arg(description),
    status                   = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata                 = sqlc.arg(metadata),
    updated_at               = now()
WHERE id = sqlc.arg(id)
  AND status != 'deprecated'
  AND ((scope='system_agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user'         AND user_id=sqlc.narg(user_id))
    OR (scope='user_agent'   AND user_id=sqlc.narg(user_id) AND agent_id=sqlc.narg(agent_id)));

-- name: DeleteSkill :exec
DELETE FROM skill
WHERE id = sqlc.arg(id)
  AND ((scope='system_agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user'         AND user_id=sqlc.narg(user_id))
    OR (scope='user_agent'   AND user_id=sqlc.narg(user_id) AND agent_id=sqlc.narg(agent_id)));

-- name: UpdateSystemSkillMetadata :exec
UPDATE skill
SET description              = sqlc.arg(description),
    status                   = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata                 = sqlc.arg(metadata),
    updated_at               = now()
WHERE id = sqlc.arg(id) AND scope = 'system';

-- name: DeleteSystemSkill :exec
DELETE FROM skill WHERE id = $1 AND scope = 'system';

-- name: UpsertSkillFile :exec
INSERT INTO skill_file (skill_id, path, content)
SELECT id, sqlc.arg(path), sqlc.arg(content)
FROM skill
WHERE id = sqlc.arg(skill_id)
  AND status != 'deprecated'
FOR NO KEY UPDATE
ON CONFLICT(skill_id, path) DO UPDATE SET content = excluded.content;

-- name: UpdateReflectOwnedUserAgentSkill :one
UPDATE skill
SET description              = sqlc.arg(description),
    status                   = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata                 = sqlc.arg(metadata),
    version                  = version + 1,
    updated_at               = now()
WHERE id = sqlc.arg(id)
  AND scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::text
  AND metadata->>'created_by' = 'reflect'
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: DeprecateReflectOwnedUserAgentSkill :one
UPDATE skill
SET status     = 'deprecated',
    version    = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::text
  AND metadata->>'created_by' = 'reflect'
  AND status = 'active'
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: RestoreReflectOwnedUserAgentSkill :one
UPDATE skill
SET status     = 'active',
    version    = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::text
  AND metadata->>'created_by' = 'reflect'
  AND status = 'deprecated'
RETURNING *;

-- name: InsertSkillChangelog :one
INSERT INTO skill_changelog (
  skill_id,
  user_id,
  agent_id,
  scope,
  action,
  version_before,
  version_after,
  metadata
)
VALUES (
  sqlc.arg(skill_id),
  sqlc.narg(user_id),
  sqlc.narg(agent_id),
  sqlc.arg(scope),
  sqlc.arg(action),
  sqlc.narg(version_before),
  sqlc.arg(version_after),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: ListSkillChangelogBySkill :many
SELECT * FROM skill_changelog
WHERE skill_id = sqlc.arg(skill_id)
ORDER BY version_after DESC, created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: GetLatestCuratorDeprecateSkillChangelog :one
SELECT *
FROM (
  SELECT *
  FROM skill_changelog
  WHERE skill_id = sqlc.arg(skill_id)
    AND user_id = sqlc.arg(user_id)::uuid
    AND agent_id = sqlc.arg(agent_id)::text
    AND scope = 'user_agent'
    AND action = 'deprecate'
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest
WHERE latest.metadata->>'curator' = 'usage';

-- name: DeleteSkillFile :exec
WITH mutable_skill AS (
  SELECT id
  FROM skill
  WHERE id = sqlc.arg(skill_id)
    AND status != 'deprecated'
  FOR NO KEY UPDATE
)
DELETE FROM skill_file
USING mutable_skill
WHERE skill_file.skill_id = mutable_skill.id
  AND skill_file.path = sqlc.arg(path);

-- name: GetSkillFile :one
SELECT * FROM skill_file WHERE skill_id = $1 AND path = $2;

-- name: ListSkillFiles :many
SELECT * FROM skill_file WHERE skill_id = $1 ORDER BY path;

-- name: GetSystemSkillByName :one
SELECT * FROM skill WHERE scope = 'system' AND name = $1;

-- name: GetSystemAgentSkillByName :one
SELECT * FROM skill WHERE scope = 'system_agent' AND agent_id = $1 AND name = $2;

-- name: GetUserSkillByName :one
SELECT * FROM skill WHERE scope = 'user' AND user_id = $1 AND name = $2;

-- name: GetUserAgentSkillByName :one
SELECT * FROM skill
WHERE scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND name = sqlc.arg(name);

-- name: DeprecateExpiredDrafts :exec
UPDATE skill
SET status = 'deprecated', updated_at = now()
WHERE status = 'draft'
  AND disable_model_invocation = false
  AND metadata->>'created-at' < sqlc.arg(cutoff)::text;

-- name: ListAllSkills :many
SELECT * FROM skill ORDER BY scope, created_at;

-- name: ListManagedActiveSkills :many
SELECT *
FROM skill
WHERE scope = ANY(sqlc.arg(scopes)::text[])
  AND status <> 'deprecated'
  AND (sqlc.narg(created_by)::text IS NULL OR metadata->>'created_by' = sqlc.narg(created_by)::text)
  AND (
    sqlc.narg(search_query)::text IS NULL
    OR lower(name) LIKE '%' || lower(sqlc.narg(search_query)::text) || '%'
    OR lower(description) LIKE '%' || lower(sqlc.narg(search_query)::text) || '%'
  )
  AND (
    (scope = 'user' AND user_id = sqlc.narg(user_id)::uuid)
    OR (scope = 'user_agent' AND user_id = sqlc.narg(user_id)::uuid AND agent_id = sqlc.narg(agent_id)::text)
    OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id)::text)
  )
  AND (
    (sqlc.narg(cursor_timestamp)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (updated_at, id) < (sqlc.narg(cursor_timestamp)::timestamptz, sqlc.narg(cursor_id)::text)
  )
ORDER BY updated_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: CountManagedActiveSkills :one
SELECT count(*)
FROM skill
WHERE scope = ANY(sqlc.arg(scopes)::text[])
  AND status <> 'deprecated'
  AND (sqlc.narg(created_by)::text IS NULL OR metadata->>'created_by' = sqlc.narg(created_by)::text)
  AND (
    sqlc.narg(search_query)::text IS NULL
    OR lower(name) LIKE '%' || lower(sqlc.narg(search_query)::text) || '%'
    OR lower(description) LIKE '%' || lower(sqlc.narg(search_query)::text) || '%'
  )
  AND (
    (scope = 'user' AND user_id = sqlc.narg(user_id)::uuid)
    OR (scope = 'user_agent' AND user_id = sqlc.narg(user_id)::uuid AND agent_id = sqlc.narg(agent_id)::text)
    OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id)::text)
  );

-- name: ListManagedRemovedSkills :many
SELECT
  s.*,
  d.created_at AS deprecated_at,
  d.metadata AS deprecate_metadata
FROM skill s
JOIN LATERAL (
  SELECT *
  FROM skill_changelog c
  WHERE c.skill_id = s.id
    AND c.action = 'deprecate'
  ORDER BY c.created_at DESC, c.id DESC
  LIMIT 1
) d ON true
WHERE s.scope = ANY(sqlc.arg(scopes)::text[])
  AND s.status = 'deprecated'
  AND (sqlc.narg(created_by)::text IS NULL OR coalesce(s.metadata->>'created_by', 'manual') = sqlc.narg(created_by)::text)
  AND (
    sqlc.narg(search_query)::text IS NULL
    OR strpos(lower(s.name), lower(sqlc.narg(search_query)::text)) > 0
    OR strpos(lower(s.description), lower(sqlc.narg(search_query)::text)) > 0
  )
  AND (
    d.metadata->>'deprecated_by' = 'manual'
    OR d.metadata->>'curator' = 'usage'
  )
  AND d.created_at > sqlc.arg(now_at)::timestamptz - interval '2160 hours'
  AND (
    (s.scope = 'user' AND s.user_id = sqlc.narg(user_id)::uuid)
    OR (s.scope = 'user_agent' AND s.user_id = sqlc.narg(user_id)::uuid AND s.agent_id = sqlc.narg(agent_id)::text)
    OR (s.scope = 'system_agent' AND s.agent_id = sqlc.narg(agent_id)::text)
  )
  AND (
    (sqlc.narg(cursor_timestamp)::timestamptz IS NULL AND sqlc.narg(cursor_id)::text IS NULL)
    OR (d.created_at, s.id) < (sqlc.narg(cursor_timestamp)::timestamptz, sqlc.narg(cursor_id)::text)
  )
ORDER BY d.created_at DESC, s.id DESC
LIMIT sqlc.arg(limit_count);

-- name: CountManagedRemovedSkills :one
SELECT count(*)
FROM skill s
JOIN LATERAL (
  SELECT *
  FROM skill_changelog c
  WHERE c.skill_id = s.id
    AND c.action = 'deprecate'
  ORDER BY c.created_at DESC, c.id DESC
  LIMIT 1
) d ON true
WHERE s.scope = ANY(sqlc.arg(scopes)::text[])
  AND s.status = 'deprecated'
  AND (sqlc.narg(created_by)::text IS NULL OR coalesce(s.metadata->>'created_by', 'manual') = sqlc.narg(created_by)::text)
  AND (
    sqlc.narg(search_query)::text IS NULL
    OR strpos(lower(s.name), lower(sqlc.narg(search_query)::text)) > 0
    OR strpos(lower(s.description), lower(sqlc.narg(search_query)::text)) > 0
  )
  AND (
    d.metadata->>'deprecated_by' = 'manual'
    OR d.metadata->>'curator' = 'usage'
  )
  AND d.created_at > sqlc.arg(now_at)::timestamptz - interval '2160 hours'
  AND (
    (s.scope = 'user' AND s.user_id = sqlc.narg(user_id)::uuid)
    OR (s.scope = 'user_agent' AND s.user_id = sqlc.narg(user_id)::uuid AND s.agent_id = sqlc.narg(agent_id)::text)
    OR (s.scope = 'system_agent' AND s.agent_id = sqlc.narg(agent_id)::text)
  );

-- name: GetLatestQualifyingManagedSkillDeprecateChangelog :one
SELECT *
FROM (
  SELECT *
  FROM skill_changelog
  WHERE skill_id = sqlc.arg(skill_id)
    AND action = 'deprecate'
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest
WHERE latest.metadata->>'deprecated_by' = 'manual'
   OR latest.metadata->>'curator' = 'usage';

-- name: DeprecateManagedSkill :one
UPDATE skill
SET status = 'deprecated',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status <> 'deprecated'
RETURNING *;

-- name: RestoreManagedSkill :one
UPDATE skill
SET status = 'active',
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'deprecated'
RETURNING *;

-- name: UpdateManagedSkill :one
UPDATE skill
SET description = sqlc.arg(description),
    status = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata = sqlc.arg(metadata),
    version = version + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status <> 'deprecated'
RETURNING *;

-- name: HasLiveManagedSkillNameConflict :one
SELECT EXISTS (
  SELECT 1
  FROM skill
  WHERE id <> sqlc.arg(id)
    AND name = sqlc.arg(name)
    AND scope = sqlc.arg(scope)
    AND COALESCE(user_id::text, '') = COALESCE(sqlc.narg(user_id)::text, '')
    AND COALESCE(agent_id, '') = COALESCE(sqlc.narg(agent_id), '')
    AND status <> 'deprecated'
) AS has_conflict;

-- name: ListActiveReflectOwnedUserAgentSkills :many
SELECT * FROM skill
WHERE scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND status = 'active'
  AND metadata->>'created_by' = 'reflect'
ORDER BY updated_at DESC, created_at DESC, id ASC;

-- name: ListRecentlyForgottenReflectSkills :many
SELECT
  s.id AS skill_id,
  s.name,
  s.description,
  s.version,
  d.id::text AS deprecated_changelog_id,
  d.created_at AS deprecated_at,
  d.metadata AS deprecate_metadata
FROM skill s
JOIN LATERAL (
  SELECT *
  FROM skill_changelog c
  WHERE c.skill_id = s.id
    AND c.user_id = s.user_id
    AND c.agent_id = s.agent_id
    AND c.scope = 'user_agent'
    AND c.action = 'deprecate'
  ORDER BY c.created_at DESC, c.id DESC
  LIMIT 1
) d ON true
WHERE s.user_id = sqlc.arg(user_id)::uuid
  AND s.agent_id = sqlc.arg(agent_id)::text
  AND s.scope = 'user_agent'
  AND s.status = 'deprecated'
  AND s.metadata->>'created_by' = 'reflect'
  AND d.metadata->>'curator' = 'usage'
ORDER BY d.created_at DESC, s.id ASC
LIMIT sqlc.arg(limit_count);
