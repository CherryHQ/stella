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

-- name: TouchKnowledgeUsage :exec
UPDATE knowledge_usage ku
SET last_used_at = now()
FROM facts f
WHERE ku.fact_id = sqlc.arg(fact_id)::uuid
  AND ku.user_id = sqlc.arg(user_id)::uuid
  AND ku.agent_id = sqlc.arg(agent_id)
  AND f.id = ku.fact_id
  AND f.user_id = ku.user_id
  AND f.agent_id = ku.agent_id
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'active'
  AND f.source = 'reflect';

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

-- name: TouchReflectSkillRuntimeUse :execrows
UPDATE skill_usage su
SET use_count = su.use_count + 1,
    last_used_at = now()
FROM skill s
WHERE su.skill_id = sqlc.arg(skill_id)
  AND su.user_id = sqlc.arg(user_id)::uuid
  AND su.agent_id = sqlc.arg(agent_id)::text
  AND s.id = su.skill_id
  AND s.user_id = su.user_id
  AND s.agent_id = su.agent_id
  AND s.scope = 'user_agent'
  AND s.status = 'active'
  AND s.disable_model_invocation = false
  AND s.metadata->>'created_by' = 'reflect';

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

-- UpdateSkillUsageHomeIdentity records only the derived logical Home identity
-- after the corresponding immutable target has been verified.
-- name: UpdateSkillUsageHomeIdentity :execrows
UPDATE skill_usage
SET scope = sqlc.arg(scope),
    name = sqlc.arg(name),
    last_content_digest = sqlc.arg(last_content_digest)
WHERE skill_id = sqlc.arg(skill_id)
  AND user_id = sqlc.arg(user_id)::uuid
  AND agent_id = sqlc.arg(agent_id)::text;

-- name: ListSkillUsageForMigration :many
SELECT * FROM skill_usage
ORDER BY skill_id;

-- name: ListReflectUsagePairs :many
SELECT DISTINCT owned.user_id, owned.agent_id
FROM (
  SELECT ku.user_id::text AS user_id, ku.agent_id
  FROM knowledge_usage ku
  UNION
  SELECT su.user_id::text AS user_id, su.agent_id
  FROM skill_usage su
) owned
ORDER BY owned.agent_id, owned.user_id;

-- The activity gate intentionally means "at least one non-archived conversation
-- had activity after this item was last used"; it does not assert recent
-- activity relative to the curator run time.
-- name: ListStaleReflectKnowledgeForCurator :many
WITH pair_activity AS (
  SELECT MAX(c.last_active)::timestamptz AS latest
  FROM ctx_conversation c
  WHERE c.user_id = sqlc.arg(user_id)::text
    AND c.agent_id = sqlc.arg(agent_id)::text
    AND c.archived = false
    AND c.kind NOT IN ('task', 'delegate', 'scheduler')
)
SELECT
  f.id::text AS fact_id,
  f.user_id::text AS user_id,
  f.agent_id,
  ku.last_used_at,
  pair_activity.latest AS pair_latest_activity_at
FROM knowledge_usage ku
JOIN facts f ON f.id = ku.fact_id
CROSS JOIN pair_activity
WHERE ku.user_id = sqlc.arg(user_id)::uuid
  AND ku.agent_id = sqlc.arg(agent_id)::text
  AND ku.last_used_at < sqlc.arg(stale_before)
  AND f.scope = 'user_agent'
  AND f.subject = 'world'
  AND f.status = 'active'
  AND f.source = 'reflect'
  AND pair_activity.latest > ku.last_used_at
ORDER BY ku.last_used_at ASC, f.id ASC;

-- The same activity gate applies here: at least one non-archived conversation
-- had activity after the skill was last used.
-- name: ListStaleReflectSkillsForCurator :many
WITH pair_activity AS (
  SELECT MAX(c.last_active)::timestamptz AS latest
  FROM ctx_conversation c
  WHERE c.user_id = sqlc.arg(user_id)::text
    AND c.agent_id = sqlc.arg(agent_id)::text
    AND c.archived = false
    AND c.kind NOT IN ('task', 'delegate', 'scheduler')
)
SELECT
  s.id AS skill_id,
  s.user_id::text AS user_id,
  s.agent_id::text AS agent_id,
  s.version,
  su.use_count,
  su.last_used_at,
  pair_activity.latest AS pair_latest_activity_at,
  CASE
    WHEN su.last_used_at < sqlc.arg(stale_before) THEN 'unused'
    ELSE 'low_use'
  END AS rule
FROM skill_usage su
JOIN skill s ON s.id = su.skill_id
CROSS JOIN pair_activity
WHERE su.user_id = sqlc.arg(user_id)::uuid
  AND su.agent_id = sqlc.arg(agent_id)::text
  AND s.scope = 'user_agent'
  AND s.status = 'active'
  AND s.metadata->>'created_by' = 'reflect'
  AND (
    su.last_used_at < sqlc.arg(stale_before)
    OR (
      su.last_used_at < sqlc.arg(low_use_before)
      AND su.use_count < sqlc.arg(low_use_max_use_count)
    )
  )
  AND pair_activity.latest > su.last_used_at
ORDER BY su.last_used_at ASC, s.id ASC;

-- name: HasEligiblePairActivityAfter :one
SELECT EXISTS (
  SELECT 1
  FROM ctx_conversation c
  WHERE c.user_id = sqlc.arg(user_id)::text
    AND c.agent_id = sqlc.arg(agent_id)::text
    AND c.archived = false
    AND c.kind NOT IN ('task', 'delegate', 'scheduler')
    AND c.last_active > sqlc.arg(after)::timestamptz
) AS has_activity;
