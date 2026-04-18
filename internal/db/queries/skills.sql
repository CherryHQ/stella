-- name: CreateSkill :one
INSERT INTO skills (id, scope, user_id, agent_id, name, description, status, disable_model_invocation, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSkill :one
SELECT * FROM skills WHERE id = ?;

-- name: ListSkillsVisible :many
SELECT * FROM skills
WHERE status != 'deprecated'
  AND disable_model_invocation = 0
  AND (
    scope = 'system'
    OR (scope = 'agent'   AND agent_id = ?)
    OR (scope = 'user'    AND user_id  = ?)
  )
ORDER BY created_at;

-- name: ResolveSkill :one
SELECT * FROM skills
WHERE name = ?
  AND status != 'deprecated'
  AND disable_model_invocation = 0
  AND (
    scope = 'system'
    OR (scope = 'agent'   AND agent_id = ?)
    OR (scope = 'user'    AND user_id  = ?)
  )
ORDER BY
  CASE scope
    WHEN 'user'   THEN 1
    WHEN 'agent'  THEN 2
    WHEN 'system' THEN 3
  END ASC
LIMIT 1;

-- name: UpdateSkillMetadata :exec
UPDATE skills
SET description              = ?,
    status                   = ?,
    disable_model_invocation = ?,
    metadata                 = ?,
    updated_at               = datetime('now')
WHERE id = ?;

-- name: DeleteSkill :exec
DELETE FROM skills WHERE id = ?;

-- name: UpsertSkillFile :exec
INSERT INTO skill_files (skill_id, path, content)
VALUES (?, ?, ?)
ON CONFLICT(skill_id, path) DO UPDATE SET content = excluded.content;

-- name: DeleteSkillFile :exec
DELETE FROM skill_files WHERE skill_id = ? AND path = ?;

-- name: GetSkillFile :one
SELECT * FROM skill_files WHERE skill_id = ? AND path = ?;

-- name: ListSkillFiles :many
SELECT * FROM skill_files WHERE skill_id = ? ORDER BY path;

-- name: GetSystemSkillByName :one
SELECT * FROM skills WHERE scope = 'system' AND name = ?;

-- name: GetAgentSkillByName :one
SELECT * FROM skills WHERE scope = 'agent' AND agent_id = ? AND name = ?;

-- name: GetUserSkillByName :one
SELECT * FROM skills WHERE scope = 'user' AND user_id = ? AND name = ?;

-- name: DeprecateExpiredDrafts :exec
UPDATE skills
SET status = 'deprecated', updated_at = datetime('now')
WHERE status = 'draft'
  AND json_extract(metadata, '$."created-at"') < ?;

-- name: ListAllSkills :many
SELECT * FROM skills ORDER BY scope, created_at;
