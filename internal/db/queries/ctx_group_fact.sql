-- name: ListActiveGroupFacts :many
SELECT *
FROM ctx_group_fact
WHERE group_id = sqlc.arg(group_id)
  AND status = 'active'
ORDER BY subject ASC, subject_id ASC NULLS FIRST, created_at ASC, id ASC;

-- name: ListGroupFactsByIDsForUpdate :many
SELECT *
FROM ctx_group_fact
WHERE group_id = sqlc.arg(group_id)
  AND id = ANY(sqlc.arg(fact_ids)::uuid[])
ORDER BY id ASC
FOR UPDATE;

-- name: InsertGroupFact :one
INSERT INTO ctx_group_fact (
  id, group_id, subject, subject_id, content, status, source
)
VALUES (
  sqlc.arg(id),
  sqlc.arg(group_id),
  sqlc.arg(subject),
  sqlc.narg(subject_id),
  sqlc.arg(content),
  sqlc.arg(status),
  sqlc.arg(source)
)
RETURNING *;

-- name: DeprecateGroupFact :one
UPDATE ctx_group_fact
SET status = 'deprecated',
    updated_at = now()
WHERE group_id = sqlc.arg(group_id)
  AND id = sqlc.arg(id)
  AND status = 'active'
RETURNING *;

-- name: InsertGroupFactChangelog :exec
INSERT INTO ctx_group_fact_changelog (
  id,
  group_id,
  fact_id,
  action,
  source,
  group_version_before,
  group_version_after,
  before_state,
  after_state
)
VALUES (
  sqlc.arg(id),
  sqlc.arg(group_id),
  sqlc.arg(fact_id),
  sqlc.arg(action),
  sqlc.arg(source),
  sqlc.arg(group_version_before),
  sqlc.arg(group_version_after),
  sqlc.narg(before_state),
  sqlc.narg(after_state)
);

-- name: ListGroupFactChangelog :many
SELECT *
FROM ctx_group_fact_changelog
WHERE group_id = sqlc.arg(group_id)
ORDER BY group_version_after DESC, created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);
