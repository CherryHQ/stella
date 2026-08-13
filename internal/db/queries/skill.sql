-- name: CreateSkill :one
INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetSkillByID :one
SELECT * FROM skill WHERE id = $1;

-- name: ListSkillIdentityVisible :many
SELECT * FROM skill
WHERE scope = 'system'
   OR (scope = 'system_agent' AND agent_id = sqlc.narg(agent_id))
   OR (scope = 'user'         AND user_id = sqlc.narg(user_id))
   OR (scope = 'user_agent'   AND user_id = sqlc.narg(user_id) AND agent_id = sqlc.narg(agent_id))
ORDER BY CASE scope
    WHEN 'user_agent'   THEN 1
    WHEN 'user'         THEN 2
    WHEN 'system_agent' THEN 3
    WHEN 'system'       THEN 4
  END, created_at
LIMIT 10001;

-- name: ListSkillsByScope :many
SELECT * FROM skill
WHERE scope = sqlc.arg(scope)
  AND coalesce(user_id::text, '') = coalesce(sqlc.narg(user_id)::text, '')
  AND coalesce(agent_id, '') = coalesce(sqlc.narg(agent_id), '')
ORDER BY created_at
LIMIT 10001;

-- name: DeleteSkill :exec
DELETE FROM skill
WHERE id = sqlc.arg(id)
  AND ((scope='system')
    OR (scope='system_agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user'         AND user_id=sqlc.narg(user_id))
    OR (scope='user_agent'   AND user_id=sqlc.narg(user_id) AND agent_id=sqlc.narg(agent_id)));

-- name: InsertSkillChangelog :one
INSERT INTO skill_changelog (
  skill_id,
  user_id,
  agent_id,
  scope,
  action,
  version_before,
  version_after,
  content_digest,
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
  sqlc.narg(content_digest),
  sqlc.arg(metadata)
)
RETURNING *;

-- name: ListSkillChangelogBySkill :many
SELECT * FROM skill_changelog
WHERE skill_id = sqlc.arg(skill_id)
ORDER BY version_after DESC, created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: GetUserAgentSkillByName :one
SELECT * FROM skill
WHERE scope = 'user_agent'
  AND user_id = sqlc.arg(user_id)
  AND agent_id = sqlc.arg(agent_id)
  AND name = sqlc.arg(name);
