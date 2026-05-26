-- name: CreateSkill :one
INSERT INTO skill (id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata, org_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSkill :one
SELECT * FROM skill
WHERE id = sqlc.arg(id)
  AND org_id = sqlc.arg(org_id)
  AND ((sqlc.narg(agent_id) IS NULL AND sqlc.narg(user_id) IS NULL) OR scope='system' OR (scope='agent' AND agent_id=sqlc.narg(agent_id)) OR (scope='user' AND user_id=sqlc.narg(user_id)));

-- name: ListSkillsVisible :many
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND status != 'deprecated'
  AND disable_model_invocation = 0
  AND (
    scope = 'system'
    OR (scope = 'agent'   AND agent_id = sqlc.narg(agent_id))
    OR (scope = 'user'    AND user_id = sqlc.narg(user_id) AND (
      (sqlc.narg(agent_id) IS NULL AND agent_id IS NULL)
      OR agent_id = sqlc.narg(agent_id)
    ))
  )
ORDER BY created_at;

-- name: ListSkillsForAgentContext :many
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND status != 'deprecated'
  AND (
    scope = 'system'
    OR (scope = 'agent' AND agent_id = sqlc.arg(agent_id))
    OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND agent_id = sqlc.arg(agent_id))
  )
ORDER BY scope, created_at;

-- name: ListSkillsForAdmin :many
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND (scope != 'user'
       OR user_id = sqlc.arg(user_id))
ORDER BY scope, created_at;

-- name: ListSkillsForUser :many
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND status != 'deprecated'
  AND (
    scope = 'system'
    OR (scope = 'agent' AND instr(',' || sqlc.arg(agent_ids_csv) || ',', ',' || agent_id || ',') > 0)
    OR (scope = 'user' AND user_id = sqlc.arg(user_id) AND (
      agent_id IS NULL
      OR instr(',' || sqlc.arg(agent_ids_csv) || ',', ',' || agent_id || ',') > 0
    ))
  )
ORDER BY scope, created_at;

-- name: ResolveSkill :one
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND name = sqlc.arg(name)
  AND status != 'deprecated'
  AND disable_model_invocation = 0
  AND (
    scope = 'system'
    OR (scope = 'agent'   AND agent_id = sqlc.narg(agent_id))
    OR (scope = 'user'    AND user_id = sqlc.narg(user_id) AND (
      (sqlc.narg(agent_id) IS NULL AND agent_id IS NULL)
      OR agent_id = sqlc.narg(agent_id)
    ))
  )
ORDER BY
  CASE scope
    WHEN 'user'   THEN 1
    WHEN 'agent'  THEN 2
    WHEN 'system' THEN 3
  END ASC
LIMIT 1;

-- name: UpdateSkillMetadata :exec
UPDATE skill
SET description              = sqlc.arg(description),
    status                   = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata                 = sqlc.arg(metadata),
    updated_at               = datetime('now')
WHERE id = sqlc.arg(id)
  AND org_id = sqlc.arg(org_id)
  AND ((scope='agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user' AND user_id=sqlc.narg(user_id)));

-- name: DeleteSkill :exec
DELETE FROM skill
WHERE id = sqlc.arg(id)
  AND org_id = sqlc.arg(org_id)
  AND ((scope='agent' AND agent_id=sqlc.narg(agent_id))
    OR (scope='user' AND user_id=sqlc.narg(user_id)));

-- name: UpdateSystemSkillMetadata :exec
UPDATE skill
SET description              = sqlc.arg(description),
    status                   = sqlc.arg(status),
    disable_model_invocation = sqlc.arg(disable_model_invocation),
    metadata                 = sqlc.arg(metadata),
    updated_at               = datetime('now')
WHERE id = sqlc.arg(id) AND scope = 'system' AND org_id = sqlc.arg(org_id);

-- name: DeleteSystemSkill :exec
DELETE FROM skill WHERE id = ? AND scope = 'system' AND org_id = ?;

-- name: UpsertSkillFile :exec
INSERT INTO skill_file (skill_id, path, content)
VALUES (?, ?, ?)
ON CONFLICT(skill_id, path) DO UPDATE SET content = excluded.content;

-- name: DeleteSkillFile :exec
DELETE FROM skill_file WHERE skill_id = ? AND path = ?;

-- name: GetSkillFile :one
SELECT * FROM skill_file WHERE skill_id = ? AND path = ?;

-- name: ListSkillFiles :many
SELECT * FROM skill_file WHERE skill_id = ? ORDER BY path;

-- name: GetSystemSkillByName :one
SELECT * FROM skill WHERE scope = 'system' AND name = ? AND org_id = ?;

-- name: GetAgentSkillByName :one
SELECT * FROM skill WHERE scope = 'agent' AND agent_id = ? AND name = ?;

-- name: GetUserSkillByName :one
SELECT * FROM skill WHERE scope = 'user' AND user_id = ? AND name = ?;

-- name: DeprecateExpiredDrafts :exec
UPDATE skill
SET status = 'deprecated', updated_at = datetime('now')
WHERE status = 'draft'
  AND disable_model_invocation = 0
  AND org_id = ?
  AND json_extract(metadata, '$."created-at"') < ?;

-- name: ListActiveKnowledgeByType :many
SELECT * FROM skill
WHERE org_id = sqlc.arg(org_id)
  AND disable_model_invocation = 1
  AND status = 'active'
  AND (
    scope = 'system'
    OR (scope = 'agent' AND agent_id = sqlc.arg(agent_id))
    OR (scope = 'user'  AND user_id  = sqlc.arg(user_id) AND agent_id = sqlc.arg(agent_id))
  )
  AND (sqlc.arg(knowledge_type) = '' OR metadata LIKE '%"knowledge_type":"' || sqlc.arg(knowledge_type) || '"%')
ORDER BY created_at DESC;

-- name: ExpireKnowledgeDraftsByType :exec
UPDATE skill
SET status = 'deprecated', updated_at = datetime('now')
WHERE status = 'draft'
  AND disable_model_invocation = 1
  AND org_id = sqlc.arg(org_id)
  AND metadata LIKE '%"knowledge_type":"' || sqlc.arg(knowledge_type) || '"%'
  AND json_extract(metadata, '$."created-at"') < sqlc.arg(cutoff);

-- name: ListAllSkills :many
SELECT * FROM skill WHERE org_id = ? ORDER BY scope, created_at;
