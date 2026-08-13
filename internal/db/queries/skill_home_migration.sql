-- name: GetSkillHomeMigration :one
SELECT * FROM skill_home_migration WHERE id = $1;

-- name: ListSkillHomeMigrationSource :many
SELECT * FROM skill
ORDER BY scope, COALESCE(user_id::text, ''), COALESCE(agent_id, ''), name, id
LIMIT 10001;

-- name: ListSkillHomeMigrationSourceFile :many
SELECT * FROM skill_file
WHERE skill_id = $1
ORDER BY path
LIMIT 513;

-- name: GetSkillHomeMigrationSourceFileStats :one
SELECT
  COUNT(*)::bigint AS file_count,
  COALESCE(SUM(octet_length(content)), 0)::bigint AS content_bytes,
  COALESCE(MAX(octet_length(content)), 0)::bigint AS max_content_bytes,
  COALESCE(MAX(octet_length(path)), 0)::bigint AS max_path_bytes
FROM skill_file
WHERE skill_id = $1;

-- name: GetSkillHomeMigrationReadiness :one
SELECT
  (SELECT count(*)::bigint FROM skill_file) AS legacy_file_count,
  (SELECT count(*)::bigint FROM skill
   WHERE description <> '' OR status <> 'active' OR disable_model_invocation
      OR metadata <> '{}' OR version <> 1 OR updated_at <> created_at) AS unnormalized_skill_count,
  (SELECT count(*)::bigint FROM skill_usage WHERE content_digest IS NULL) AS missing_usage_digest_count;

-- name: CompleteSkillHomeMigration :one
WITH expected_usage AS MATERIALIZED (
  SELECT count(*)::bigint AS count FROM skill_usage
), removed AS (
  DELETE FROM skill_file RETURNING octet_length(content)::bigint AS content_bytes
), normalized AS (
  UPDATE skill
  SET description = '', status = 'active', disable_model_invocation = false,
      metadata = '{}', version = 1, updated_at = created_at
  RETURNING 1
), usage_evidence AS (
  UPDATE skill_usage u
  SET content_digest = item->>'content_digest'
  FROM jsonb_array_elements(sqlc.arg(inventory)::jsonb) item
  WHERE item->>'skill_id' = u.skill_id
  RETURNING 1
), marker AS (
  INSERT INTO skill_home_migration (
    id, state, source_skill_count, source_file_count, source_content_bytes,
    source_inventory_digest, inventory, writers_stopped_attested_at,
    backup_verified_attested_at, completed_at
  )
  VALUES (
    sqlc.arg(id), 'completed', sqlc.arg(source_skill_count),
    sqlc.arg(source_file_count), sqlc.arg(source_content_bytes),
    sqlc.arg(source_inventory_digest), sqlc.arg(inventory),
    sqlc.arg(attested_at), sqlc.arg(attested_at), sqlc.arg(completed_at)
  )
  RETURNING *
)
SELECT marker.*,
  (SELECT count(*)::bigint FROM removed) AS removed_file_count,
  (SELECT COALESCE(sum(content_bytes), 0)::bigint FROM removed) AS removed_content_bytes,
  (SELECT count(*)::bigint FROM normalized) AS normalized_skill_count,
  (SELECT count(*)::bigint FROM usage_evidence) AS updated_usage_count,
  (SELECT count FROM expected_usage) AS expected_usage_count
FROM marker;
