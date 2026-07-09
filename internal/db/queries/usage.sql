-- name: UpsertKnowledgeUsage :exec
INSERT INTO knowledge_usage (fact_id, user_id, agent_id, last_used_at)
SELECT f.id, f.user_id, f.agent_id, now()
FROM facts f
WHERE f.id = sqlc.arg(fact_id)::uuid
  AND f.user_id = sqlc.arg(user_id)::uuid
  AND f.agent_id = sqlc.arg(agent_id)
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'active'
  AND f.source = 'reflect'
ON CONFLICT (fact_id) DO UPDATE
SET last_used_at = excluded.last_used_at;

-- name: GetKnowledgeUsageForUpdate :one
SELECT *
FROM knowledge_usage
WHERE fact_id = sqlc.arg(fact_id)::uuid
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)
FOR UPDATE;

-- name: DeleteKnowledgeUsage :exec
DELETE FROM knowledge_usage
WHERE fact_id = sqlc.arg(fact_id);

-- name: UpsertSkillUsageOnReflectCreate :exec
INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at)
VALUES (sqlc.arg(skill_id), sqlc.arg(user_id), sqlc.arg(agent_id), 1, now())
ON CONFLICT (skill_id) DO UPDATE
SET user_id = excluded.user_id,
    agent_id = excluded.agent_id,
    use_count = excluded.use_count,
    last_used_at = excluded.last_used_at;

-- name: RefreshSkillUsageOnReflectPatch :exec
INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at)
SELECT s.id, s.user_id, s.agent_id, 0, now()
FROM skill s
WHERE s.id = sqlc.arg(skill_id)
  AND s.user_id = sqlc.arg(user_id)::uuid
  AND s.agent_id = sqlc.arg(agent_id)::text
  AND s.scope = 'user_agent'
  AND s.status = 'active'
  AND s.metadata->>'created_by' = 'reflect'
ON CONFLICT (skill_id) DO UPDATE
SET last_used_at = excluded.last_used_at;

-- name: TouchReflectSkillRuntimeUse :exec
INSERT INTO skill_usage (skill_id, user_id, agent_id, use_count, last_used_at)
SELECT s.id, s.user_id, s.agent_id, 1, now()
FROM skill s
WHERE s.id = sqlc.arg(skill_id)
  AND s.user_id = sqlc.arg(user_id)::uuid
  AND s.agent_id = sqlc.arg(agent_id)::text
  AND s.scope = 'user_agent'
  AND s.status = 'active'
  AND s.disable_model_invocation = false
  AND s.metadata->>'created_by' = 'reflect'
ON CONFLICT (skill_id) DO UPDATE
SET use_count = skill_usage.use_count + 1,
    last_used_at = excluded.last_used_at
WHERE skill_usage.user_id = excluded.user_id
  AND skill_usage.agent_id = excluded.agent_id;

-- name: GetSkillUsageForUpdate :one
SELECT *
FROM skill_usage
WHERE skill_id = sqlc.arg(skill_id)
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::text
FOR UPDATE;

-- name: DeleteSkillUsage :exec
DELETE FROM skill_usage
WHERE skill_id = sqlc.arg(skill_id);

-- The activity gate intentionally means "at least one non-archived conversation
-- had activity after this item was last used"; it does not assert recent
-- activity relative to the curator run time.
-- name: ListStaleReflectKnowledgeForCurator :many
SELECT
  f.id::text AS fact_id,
  f.user_id::text AS user_id,
  f.agent_id,
  ku.last_used_at,
  MAX(c.last_active)::timestamptz AS pair_latest_activity_at
FROM knowledge_usage ku
JOIN facts f ON f.id = ku.fact_id
JOIN ctx_conversation c
  ON c.user_id = f.user_id::text
 AND c.agent_id = f.agent_id
 AND c.archived = false
WHERE ku.last_used_at < sqlc.arg(stale_before)
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'active'
  AND f.source = 'reflect'
GROUP BY f.id, f.user_id, f.agent_id, ku.last_used_at
HAVING MAX(c.last_active) > ku.last_used_at
ORDER BY ku.last_used_at ASC, f.id ASC;

-- The same activity gate applies here: at least one non-archived conversation
-- had activity after the skill was last used.
-- name: ListStaleReflectSkillsForCurator :many
SELECT
  s.id AS skill_id,
  s.user_id::text AS user_id,
  s.agent_id::text AS agent_id,
  s.version,
  su.use_count,
  su.last_used_at,
  MAX(c.last_active)::timestamptz AS pair_latest_activity_at,
  CASE
    WHEN su.last_used_at < sqlc.arg(stale_before) THEN 'unused'
    ELSE 'low_use'
  END AS rule
FROM skill_usage su
JOIN skill s ON s.id = su.skill_id
JOIN ctx_conversation c
  ON c.user_id = s.user_id::text
 AND c.agent_id = s.agent_id
 AND c.archived = false
WHERE s.scope = 'user_agent'
  AND s.status = 'active'
  AND s.metadata->>'created_by' = 'reflect'
  AND (
    su.last_used_at < sqlc.arg(stale_before)
    OR (
      su.last_used_at < sqlc.arg(low_use_before)
      AND su.use_count < sqlc.arg(low_use_max_use_count)
    )
  )
GROUP BY s.id, s.user_id, s.agent_id, s.version, su.use_count, su.last_used_at
HAVING MAX(c.last_active) > su.last_used_at
ORDER BY su.last_used_at ASC, s.id ASC;
